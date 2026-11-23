package agentspool

// Cross-language tests: the real agent writes, this package reads. They
// need a JDK and the agent build, so they run only when `mage agentTest`
// sets the environment (absolute paths):
//
//	IORMESH_AGENT_TEST_CP  classpath with iormesh.bridge.RingStress
//	IORMESH_AGENT_JAR      bin/iormesh-agent.jar
//	IORMESH_WORKLOAD_JAR   bin/workload.jar

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func javaEnv(t *testing.T, names ...string) []string {
	t.Helper()
	var vals []string
	for _, n := range names {
		v := os.Getenv(n)
		if v == "" {
			t.Skipf("%s not set; run through `mage agentTest`", n)
		}
		vals = append(vals, v)
	}
	return vals
}

// TestJavaRingStress: eight Java threads write to a 64 KiB ring while
// this reader drains it — the CAS contended, the ring lapping — and every
// record is either delivered in its writer's order or counted as lost.
func TestJavaRingStress(t *testing.T) {
	cp := javaEnv(t, "IORMESH_AGENT_TEST_CP")[0]
	s := newStressCheck(t)
	r, written := runRingStress(t, cp, "raw", s.record)
	l := r.Losses()
	got := s.delivered + l.DroppedBrackets + l.DroppedStacks + l.Abandoned
	if got != written || l.Unknown+l.Malformed+l.DroppedThreads+l.DroppedWatermarks != 0 {
		t.Errorf("delivered %d + losses %+v != written %d", s.delivered, l, written)
	}
	if s.delivered*bracketLen < 10*testCap {
		t.Errorf("only %d records delivered: the ring hardly wrapped", s.delivered)
	}
	t.Logf("delivered %d, losses %+v", s.delivered, l)
}

// TestJavaHooksStress: eight Java threads close brackets through Hooks
// from one call site while the watermark thread runs every millisecond
// and this reader drains a 64 KiB ring. No BRACKET may follow a WATERMARK
// it exited before, and every thread and stack id resolves. Mutation
// check (by hand, 1b4): without the closing mark in Hooks.close it failed
// 6 of 6 runs on x86-64. The fences cannot be checked this way there —
// x86 already orders a volatile store before later loads — so their
// absence would show only on weaker hardware (AArch64), and even then
// only probabilistically.
func TestJavaHooksStress(t *testing.T) {
	cp := javaEnv(t, "IORMESH_AGENT_TEST_CP")[0]
	a := newAgentCheck(t)
	r, written := runRingStress(t, cp, "hooks", a.record)
	l := r.Losses()
	a.resolve(l)
	if got := uint64(len(a.named)) + l.DroppedBrackets + l.Abandoned; got != written {
		t.Errorf("brackets delivered %d + losses %+v != written %d", len(a.named), l, written)
	}
	if a.watermarks < 10 || l.Unknown+l.Malformed != 0 {
		t.Errorf("%d watermarks, losses %+v", a.watermarks, l)
	}
	t.Logf("brackets %d, watermarks %d, losses %+v", len(a.named), a.watermarks, l)
}

// runRingStress runs RingStress in mode with a live reader handing every
// record to fn, and returns the drained ring and the written count.
func runRingStress(t *testing.T, cp, mode string, fn func(Record)) (*Ring, uint64) {
	t.Helper()
	cmd := exec.Command("java", "-cp", cp, "iormesh.bridge.RingStress", mode,
		t.TempDir(), strconv.Itoa(testCap), "8", "20000")
	stdin, lines := startJava(t, cmd)
	ready := <-lines
	path, ok := strings.CutPrefix(ready, "ready ")
	if !ok {
		t.Fatalf("RingStress: %q, want ready <path>", ready)
	}
	r := openRing(t, path)
	if _, err := io.WriteString(stdin, "go\n"); err != nil {
		t.Fatal(err)
	}
	written := drainUntilDone(t, r, lines, fn)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("RingStress: %v", err)
	}
	if _, err := r.Drain(fn); err != nil {
		t.Fatal(err)
	}
	return r, written
}

// openWhileRunning opens the ring at path as soon as the agent created
// and set it up, polling every millisecond; it fails the test if the
// JVM exits (exited delivers) first.
func openWhileRunning(t *testing.T, path string, exited <-chan error) *Ring {
	t.Helper()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		r, err := Open(path)
		if err == nil {
			t.Cleanup(func() { _ = r.Close() })
			return r
		}
		if !errors.Is(err, ErrNotReady) && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case err := <-exited:
			t.Fatalf("the JVM exited (%v) before its ring %s could be opened", err, path)
		case <-tick.C:
		}
	}
}

// startJava starts cmd and returns its stdin and a channel of its stdout
// lines, closed at EOF.
func startJava(t *testing.T, cmd *exec.Cmd) (io.WriteCloser, <-chan string) {
	t.Helper()
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	lines := make(chan string, 4)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	return stdin, lines
}

// drainUntilDone drains r while the writer runs and returns the count
// from its final "written N" line.
func drainUntilDone(t *testing.T, r *Ring, lines <-chan string, fn func(Record)) uint64 {
	t.Helper()
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("RingStress exited without a written line")
			}
			n, err := strconv.ParseUint(strings.TrimPrefix(line, "written "), 10, 64)
			if err != nil {
				t.Fatalf("RingStress: %q: %v", line, err)
			}
			return n
		default:
		}
		if _, err := r.Drain(fn); err != nil {
			t.Fatal(err)
		}
	}
}

// stressCheck checks RingStress's records as they arrive: each writer's
// brackets in sequence order, its stack frames intact.
type stressCheck struct {
	t         *testing.T
	last      map[int32]uint64
	delivered uint64
}

func newStressCheck(t *testing.T) *stressCheck {
	return &stressCheck{t: t, last: map[int32]uint64{}}
}

func (s *stressCheck) record(rec Record) {
	s.delivered++
	switch v := rec.(type) {
	case Bracket:
		if last, seen := s.last[v.Hook]; seen && v.StackID <= last {
			s.t.Errorf("writer %d: seq %d after %d", v.Hook, v.StackID, last)
		}
		s.last[v.Hook] = v.StackID
		if v.KTID != 999+v.Hook || v.ExitMono != v.EnterMono+1 {
			s.t.Errorf("bracket fields %+v", v)
		}
	case Stack:
		suffix := fmt.Sprintf("(Größe.java:%d)", v.ID)
		if len(v.Frames) != 2 || !strings.HasPrefix(v.Frames[0], "stress#w") ||
			!strings.HasSuffix(v.Frames[0], suffix) ||
			!slices.Equal(v.Classes, []uint64{v.ID + 1, 1}) {
			s.t.Errorf("stack %+v", v)
		}
	default:
		s.t.Errorf("unexpected record %+v", rec)
	}
}

// TestJavaAgentWorkload runs the workload's file-basic scenario under
// the agent with a spool directory and reads its ring: mapped while the
// JVM runs, as a tracer does, since the agent's shutdown hook unlinks
// the file after its last watermark (task 2j4) — which the test checks.
func TestJavaAgentWorkload(t *testing.T) {
	env := javaEnv(t, "IORMESH_AGENT_JAR", "IORMESH_WORKLOAD_JAR")
	dir := t.TempDir()
	debug := filepath.Join(t.TempDir(), "hooks.tsv")
	cmd := exec.Command("java", "-javaagent:"+env[0]+"=spool="+dir+",debug="+debug,
		"-jar", env[1], "file-basic")
	cmd.Stderr = io.Discard
	var out strings.Builder
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	pid := cmd.Process.Pid
	path := filepath.Join(dir, strconv.Itoa(pid)+".ring")
	r := openWhileRunning(t, path, exited)
	if err := <-exited; err != nil {
		t.Fatalf("file-basic under the agent: %v: %s", err, out.String())
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("the JVM left its ring behind: %v", err)
	}
	if r.Header().PID != uint64(pid) {
		t.Errorf("header pid %d, want %d", r.Header().PID, pid)
	}
	a := newAgentCheck(t)
	if _, err := r.Drain(a.record); err != nil {
		t.Fatal(err)
	}
	l := r.Losses()
	if l.Total() != 0 || r.Pending() != 0 {
		t.Errorf("losses %+v, pending %d", l, r.Pending())
	}
	a.resolve(l)
	a.finish(readDebugCounts(t, debug))
	// The class-load map (task fe4): the scenario's own frames name
	// classes the app loader read from the workload jar (the JVM resolves
	// the class path's symlinks), the JDK's the runtime image.
	jar, err := filepath.EvalSymlinks(env[1])
	if err != nil {
		t.Fatal(err)
	}
	if src := a.codeSources(); !src[jar+" app"] || !src["jrt:/java.base bootstrap"] {
		t.Errorf("code sources %v: want the workload jar (app) and java.base", src)
	}
}

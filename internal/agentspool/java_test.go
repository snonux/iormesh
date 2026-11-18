package agentspool

// Cross-language tests: the real agent writes, this package reads. They
// need a JDK and the agent build, so they run only when `mage agentTest`
// sets the environment (absolute paths):
//
//	IORMESH_AGENT_TEST_CP  classpath with iormesh.bridge.RingStress
//	IORMESH_AGENT_JAR      iormesh-agent.jar
//	IORMESH_WORKLOAD_JAR   workload.jar

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
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
	cmd := exec.Command("java", "-cp", cp, "iormesh.bridge.RingStress",
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
	s := newStressCheck(t)
	written := drainUntilDone(t, r, lines, s.record)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("RingStress: %v", err)
	}
	if _, err := r.Drain(s.record); err != nil {
		t.Fatal(err)
	}
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
			!strings.HasSuffix(v.Frames[0], suffix) {
			s.t.Errorf("stack %+v", v)
		}
	default:
		s.t.Errorf("unexpected record %+v", rec)
	}
}

// TestJavaAgentWorkload runs the workload's file-basic scenario under
// the agent with a spool directory and reads the ring it leaves behind.
func TestJavaAgentWorkload(t *testing.T) {
	env := javaEnv(t, "IORMESH_AGENT_JAR", "IORMESH_WORKLOAD_JAR")
	dir := t.TempDir()
	debug := filepath.Join(t.TempDir(), "hooks.tsv")
	cmd := exec.Command("java", "-javaagent:"+env[0]+"=spool="+dir+",debug="+debug,
		"-jar", env[1], "file-basic")
	cmd.Stderr = io.Discard
	if out, err := cmd.Output(); err != nil {
		t.Fatalf("file-basic under the agent: %v: %s", err, out)
	}
	pid := cmd.Process.Pid
	r := openRing(t, filepath.Join(dir, strconv.Itoa(pid)+".ring"))
	if r.Header().PID != uint64(pid) {
		t.Errorf("header pid %d, want %d", r.Header().PID, pid)
	}
	a := newAgentCheck(t)
	if _, err := r.Drain(a.record); err != nil {
		t.Fatal(err)
	}
	if l := r.Losses(); l.Total() != 0 || r.Pending() != 0 {
		t.Errorf("losses %+v, pending %d", l, r.Pending())
	}
	a.finish(readDebugCounts(t, debug))
}

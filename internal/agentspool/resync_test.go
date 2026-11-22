package agentspool

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestJavaSecondReaderResync (task 5j4): two tracer sessions in a row on
// one live JVM. The agent writes its dictionary (HOOK, THREAD, STACK,
// CLASS) once and the first reader consumes it, so before the fix the
// second session read brackets only — no hook table, hence no probes and
// attribution none for the JVM's life. The second reader's open must
// make the agent resend it: session 2 gets the whole hook table again
// and, for the brackets after it, their THREAD, STACK and CLASS records.
func TestJavaSecondReaderResync(t *testing.T) {
	env := javaEnv(t, "IORMESH_AGENT_JAR", "IORMESH_AGENT_TEST_CP")
	cp, _, _ := strings.Cut(env[1], ":") // the test classes, IoLoop
	spool := t.TempDir()
	cmd := exec.Command("java", "-javaagent:"+env[0]+"=spool="+spool, "-cp", cp,
		"iormesh.loop.IoLoop", t.TempDir())
	stdin, lines := startJava(t, cmd)
	if l := <-lines; l != "looping" {
		t.Fatalf("IoLoop: %q, want looping", l)
	}
	path := filepath.Join(spool, strconv.Itoa(cmd.Process.Pid)+".ring")
	first := readSession(t, path, time.Second)
	if len(first.hooks) == 0 || len(first.named) == 0 {
		t.Fatalf("session 1: %d hooks, %d brackets", len(first.hooks), len(first.named))
	}
	second := readSession(t, path, 2*time.Second)
	if len(second.hooks) != len(first.hooks) {
		t.Fatalf("session 2: %d HOOK rows, session 1 had %d", len(second.hooks),
			len(first.hooks))
	}
	if len(second.named) == 0 {
		t.Fatal("session 2: no bracket after the resent hook table")
	}
	// No drops, so resolve may excuse no missing THREAD, STACK or CLASS.
	if second.losses.Total() != 0 {
		t.Errorf("session 2: losses %+v", second.losses)
	}
	second.check.resolve(second.losses)
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("IoLoop: %v", err)
	}
	t.Logf("session 1: %d hooks, %d brackets; session 2: %d hooks, %d brackets after them",
		len(first.hooks), len(first.named), len(second.hooks), len(second.named))
}

// TestOpenBumpsReaderEpoch: every open adds one to the header's reader
// epoch (task 5j4), the word the agent polls to resend its dictionary;
// the writer's own header words stay as they were.
func TestOpenBumpsReaderEpoch(t *testing.T) {
	path, _ := newTestRing(t, testCap)
	for want := uint64(1); want <= 2; want++ {
		r := openRing(t, path)
		if got := r.word(offEpoch); got != want {
			t.Errorf("open %d: epoch %d", want, got)
		}
		if h := r.Header(); h.Version != Version || h.Capacity != testCap {
			t.Errorf("open %d: header %+v", want, h)
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// session is what one reader of a live ring collected: every record
// went to check except the brackets read before the last HOOK row, which
// the agent wrote before it resent its dictionary and so may name
// threads and stacks this session never gets.
type session struct {
	check  *agentCheck
	hooks  map[int32]Hook
	named  []Bracket
	losses Losses
}

// readSession opens the ring as a new reader, drains it for d and closes
// it again, as a tracer session would.
func readSession(t *testing.T, path string, d time.Duration) session {
	t.Helper()
	r := openWhenReady(t, path)
	defer func() { _ = r.Close() }()
	s := session{check: newAgentCheck(t), hooks: map[int32]Hook{}}
	var pending []Bracket
	fn := func(rec Record) {
		switch v := rec.(type) {
		case Hook:
			s.hooks[v.ID] = v
			pending = pending[:0]
			s.check.hooks[v.ID] = v
		case Bracket:
			pending = append(pending, v)
		default:
			s.check.record(rec)
		}
	}
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if _, err := r.Drain(fn); err != nil {
			t.Fatal(err)
		}
	}
	s.named = pending
	s.check.named = pending
	s.losses = r.Losses()
	return s
}

// openWhenReady opens the ring once the agent has created it and written
// its header (the JVM prints "looping" after premain, so this is short).
func openWhenReady(t *testing.T, path string) *Ring {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		r, err := Open(path)
		if err == nil {
			return r
		}
		if time.Now().After(deadline) ||
			!errors.Is(err, ErrNotReady) && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("open %s: %v", path, err)
		}
	}
}

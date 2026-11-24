package integrationtests

// M2 step 10 end to end: a JVM talking to a process that is not a JVM
// (workload/echopeer, a cgo-free Go echo server it spawns) shows the
// degraded node shape — files and sockets only, every event attribution
// none, no Java fields, no agent-sourced records (docs/milestones.md M0
// "Non-Java peer"; docs/trace-scope.md "Non-Java processes";
// docs/implementation.md M2 step 10).

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/normalize"
	"iormesh/internal/schema"
)

// peerBinary is the peer's executable name: its exec event's path ends
// in it, and it is the peer's comm (8 bytes, uncut). The JVM forks the
// peer, so the child starts with the JVM's comm; the correlator renames
// it on the exec (task hd4), and the peer's exec and later events must
// carry the new name.
const peerBinary = "echopeer"

// stacksFile is cli.StacksFile, the native stack table of a
// -native-stacks run (M2 step 9).
const stacksFile = "stacks.ndjson"

// TestNonJavaPeer: the non-java-peer scenario passes its golden — the
// JVM's connect, write, read and file read, the peer's accept4, echo,
// EOF read and log write, one fused connection, both opening the log —
// the peer was followed as the JVM's forked child, and the whole run
// carries the degraded shape: every event attribution none, no Java or
// http payload anywhere (no agent is attached in M2, and none could be
// for a Go binary).
func TestNonJavaPeer(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	run := traceJudged(t, h, startScenario("non-java-peer"))
	assertTracedOrForked(t, run.Trace, run.pids)
	assertPeerTraced(t, run.Trace.Events)
	assertDegradedShape(t, run.Trace.Events)
	assertNoStacks(t, run)

	got := normalized(t, h, run, "non-java-peer")
	want := expectation(t, "non-java-peer")
	assertGolden(t, got, want)
	t.Run("wrong expectations are caught", func(t *testing.T) {
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			"the peer attributed to Java": func(x *normalize.Expectation) {
				x.Events = slices.Clone(x.Events)
				for i, e := range x.Events {
					if e.Process == "$p2" {
						x.Events[i].Attribution = schema.AttributionJava
					}
				}
			},
			"the peer's connection unresolved": func(x *normalize.Expectation) {
				x.Edges = slices.Clone(x.Edges)
				x.Edges[1].State = schema.StateUnresolved
			},
		})
	})
}

// assertPeerTraced fails unless the Go peer itself was followed: its exec
// and its accept4 are in the trace, the accept4 under the peer's own comm
// (not the JVM's, which its forked child started with). A run where only
// the JVM was traced would pass the attribution checks vacuously for the
// peer.
func assertPeerTraced(t *testing.T, events []schema.Event) {
	t.Helper()
	pid := peerPID(t, events)
	for _, e := range events {
		if e.PID == pid && e.Event == schema.KindAccept4 {
			if e.Comm != peerBinary {
				t.Fatalf("the peer's accept4 has comm %q, want %q", e.Comm, peerBinary)
			}
			return
		}
	}
	t.Fatalf("no accept4 of %s (pid %d) in the trace", peerBinary, pid)
}

// peerPID returns the pid that exec'd the peer binary. Its exec event
// must already carry the peer's comm: the kernel renamed the process
// before the exec's argv record.
func peerPID(t *testing.T, events []schema.Event) uint32 {
	t.Helper()
	for _, e := range events {
		if e.Event == schema.KindExec && e.Args.Path != nil && filepath.Base(*e.Args.Path) == peerBinary {
			if e.Comm != peerBinary {
				t.Fatalf("the peer's exec has comm %q, want %q", e.Comm, peerBinary)
			}
			return e.PID
		}
	}
	t.Fatalf("no exec of %s in the trace: the non-JVM peer was not followed", peerBinary)
	return 0
}

// assertDegradedShape checks every event of the raw trace, peer and JVM
// alike: attribution is none and no Java or http payload is set. M2 has
// no agent, so this is the shape of every process; once M3's agent
// attributes JVM events, the peer's must stay this way.
func assertDegradedShape(t *testing.T, events []schema.Event) {
	t.Helper()
	for _, e := range events {
		if e.Attribution != schema.AttributionNone || e.Java != nil || e.HTTP != nil {
			t.Errorf("event %d (%s of %s): attribution %q, java %v, http %v; want none, nil, nil",
				e.Seq, e.Event, e.Comm, e.Attribution, e.Java, e.HTTP)
		}
	}
}

// assertNoStacks: native stacks are opt-in (decision #25), so a default
// run has no stack_id on any event and writes no stack table.
func assertNoStacks(t *testing.T, run tracedRun) {
	t.Helper()
	for _, e := range run.Trace.Events {
		if e.StackID != nil {
			t.Fatalf("event %d (%s) has stack_id %d without -native-stacks", e.Seq, e.Event, *e.StackID)
		}
	}
	if _, err := os.Stat(filepath.Join(run.Dir, stacksFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s without -native-stacks: %v", stacksFile, err)
	}
}

// TestNativeStacks (M2 step 9): with -native-stacks the non-java-peer run
// still passes its golden, every stack_id names an entry of
// stacks.ndjson in the event's own process, and the Go peer — built with
// frame pointers, so the kernel can walk it — has its scenario syscalls'
// stacks symbolized down to its own functions in its own binary. The
// JVM's stacks are not judged: JIT-compiled frames do not unwind
// (docs/schema.md, "Native stacks"), which is why Java attribution comes
// from the agent.
func TestNativeStacks(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	h.NativeStacks = true
	run := traceJudged(t, h, startScenario("non-java-peer"))
	logStackSummary(t, run.Logged)
	table := readStacks(t, run)
	assertStackRefs(t, run.Trace.Events, table)
	for _, kind := range []string{schema.KindAccept4, schema.KindOpenat} {
		assertPeerStack(t, run.Trace.Events, table, kind)
	}
	assertGolden(t, normalized(t, h, run, "non-java-peer"), expectation(t, "non-java-peer"))
}

// logStackSummary logs the summary line's native stack counts (how many
// came inline, how many were uncaptured and why), so a failed stack
// assertion shows whether the kernel lost stacks (task kd4).
func logStackSummary(t *testing.T, logged string) {
	t.Helper()
	for line := range strings.Lines(logged) {
		if strings.Contains(line, "native stacks") {
			t.Logf("tracer: %s", strings.TrimSpace(line))
		}
	}
}

// readStacks reads the run's stack table strictly, by stack id.
func readStacks(t *testing.T, run tracedRun) map[uint32]schema.Stack {
	t.Helper()
	f, err := os.Open(filepath.Join(run.Dir, stacksFile))
	if err != nil {
		t.Fatalf("stack table: %v", err)
	}
	defer func() { _ = f.Close() }()
	stacks, err := schema.ReadStacks(f)
	if err != nil {
		t.Fatalf("stack table: %v", err)
	}
	table := map[uint32]schema.Stack{}
	for _, st := range stacks {
		if _, dup := table[st.StackID]; dup {
			t.Fatalf("stack_id %d written twice", st.StackID)
		}
		table[st.StackID] = st
	}
	return table
}

// assertStackRefs: every stack_id resolves, to a stack of the event's own
// process incarnation (addresses mean nothing in another address space).
func assertStackRefs(t *testing.T, events []schema.Event, table map[uint32]schema.Stack) {
	t.Helper()
	for _, e := range events {
		if e.StackID == nil {
			continue
		}
		st, ok := table[*e.StackID]
		if !ok {
			t.Fatalf("event %d (%s) names stack %d, which stacks.ndjson lacks", e.Seq, e.Event, *e.StackID)
		}
		if st.PID != e.PID || st.PIDInc != e.PIDInc {
			t.Errorf("event %d of pid %d names stack %d of pid %d", e.Seq, e.PID, st.StackID, st.PID)
		}
	}
}

// assertPeerStack: the peer's scenario event of kind (for openat its
// log file's, not the Go runtime's start-up opens) carries a stack whose
// frames include one of echopeer's own main package functions, resolved in
// the echopeer binary.
func assertPeerStack(t *testing.T, events []schema.Event, table map[uint32]schema.Stack, kind string) {
	t.Helper()
	pid := peerPID(t, events)
	for _, e := range events {
		if e.PID != pid || e.Event != kind {
			continue
		}
		if kind == schema.KindOpenat && (e.Args.Path == nil || !strings.HasSuffix(*e.Args.Path, ".log")) {
			continue
		}
		if e.StackID == nil {
			t.Fatalf("the peer's %s (seq %d) has no stack_id", kind, e.Seq)
		}
		for _, f := range table[*e.StackID].Frames {
			if strings.HasPrefix(f.Symbol, "main.") && filepath.Base(f.Module) == peerBinary {
				return
			}
		}
		t.Fatalf("the peer's %s stack has no main.* frame of %s: %+v", kind, peerBinary, table[*e.StackID].Frames)
	}
	t.Fatalf("no scenario %s of %s in the trace", kind, peerBinary)
}

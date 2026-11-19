package integrationtests

// PID reuse end to end (PLAN.md M2 acceptance; docs/testing.md, "M2
// acceptance"): a traced process exits, an untraced process takes over
// its PID while the session still runs, and the tracer neither traces the
// newcomer nor lets the old incarnation's events drift onto it, and the
// session still ends once its targets are gone although the PID lives on.
//
// The reuse is forced, not waited for: workload/edgepeer's pid-reuse role
// lands its child on exactly the freed PID with clone3(set_tid) (root,
// which the suite has anyway). No fork loop, no race with the host.

import (
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"iormesh/internal/schema"
	"iormesh/internal/target"
)

// The edgepeer brackets of the PID reuse choreography.
const (
	reuseScenario    = "pid-reuse"
	keeperScenario   = "pid-reuse-keeper"
	squatterScenario = "pid-reuse-squatter"
	squatterFile     = "pid-reuse-squatter.dat"
	targetFile       = "pid-reuse-target.dat"
	// reuseSettle is the pause between the target's exit and the reuse
	// of its PID (tracePIDReuse says why).
	reuseSettle = 500 * time.Millisecond
)

// reuseRun is one traced PID reuse run and what its choreography saw.
type reuseRun struct {
	tracedRun
	pid         int    // the target's PID, reused by the squatter
	targetInc   uint64 // the target's incarnation (start time)
	squatterInc uint64 // the squatter's, on the same PID
	// squatterLived: the squatter still held the PID, as the same
	// incarnation, when the tracer had already ended by itself.
	squatterLived bool
}

// TestPIDReuse: the target (traced) brackets file work and exits; the
// launcher (untraced) puts the squatter on the freed PID, which does file
// work of its own while the keeper (traced) keeps the session open; then
// the keeper exits and the session must end while the squatter still
// holds the PID. The trace holds the target's work under its own
// incarnation, nothing of the squatter, one process node for the PID, and
// ends with targets_exited.
func TestPIDReuse(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	peer := absPath(t, edgePeerDefault)
	run := judge(t, func() (reuseRun, error) { return tracePIDReuse(h, peer) })
	if run.targetInc == run.squatterInc {
		t.Fatalf("target and squatter on pid %d share the start time %d: the incarnations cannot be told apart",
			run.pid, run.targetInc)
	}
	if !run.squatterLived {
		t.Fatalf("the squatter on pid %d was gone before the tracer ended: the session end proves nothing", run.pid)
	}
	assertEndReason(t, run.Trace.Events, schema.EndTargetsExited)
	assertTargetKept(t, run)
	assertSquatterUntraced(t, run)
	assertOneNodeForPID(t, run)
}

// tracePIDReuse is one attempt of the choreography.
func tracePIDReuse(h *Harness, peer string) (reuseRun, error) {
	tgt, err := h.StartHelper(peer, "pid-target", "-dir", h.Tmp)
	if err != nil {
		return reuseRun{}, err
	}
	keeper, err := h.StartHeldHelper(peer, "pid-keeper")
	if err != nil {
		return reuseRun{}, err
	}
	run := reuseRun{pid: tgt.PID, tracedRun: tracedRun{pids: []int{tgt.PID, keeper.PID}}}
	if run.targetInc, err = startNS(tgt.PID); err != nil {
		return run, err
	}
	h.runs++
	run.Dir = filepath.Join(h.Dir, fmt.Sprintf("trace-%d", h.runs))
	tracer, err := h.startTracer([]*Workload{tgt, keeper}, run.Dir)
	if err != nil {
		return run, err
	}
	if err := release([]*Workload{tgt, keeper}); err != nil {
		return run, err
	}
	if err := tgt.proc.wait(runTimeout); err != nil {
		return run, err
	}
	// The correlator reads a target's incarnation from /proc when it
	// handles the target's first record, so the reuse waits until that
	// surely happened (the reader lags well under a millisecond): the
	// test is about the tracer telling two live incarnations apart, not
	// about a correlator that met the target only after it was gone.
	time.Sleep(reuseSettle)
	if err := squatWhileTracing(h, peer, keeper, tracer, &run); err != nil {
		return run, err
	}
	run.Logged = tracer.stderr.String()
	if run.Loss, err = ParseKernelLoss(run.Logged); err != nil {
		return run, err
	}
	run.Trace, err = ReadTrace(run.Dir)
	return run, err
}

// squatWhileTracing reuses the exited target's PID, ends the keeper and
// waits for the tracer to end by itself, then lets the squatter go —
// recording whether it still held the PID when the tracer was done.
func squatWhileTracing(h *Harness, peer string, keeper *Workload, tracer *process, run *reuseRun) error {
	launcher, stdin, err := startLauncher(h, peer, run.pid)
	if err != nil {
		return err
	}
	if _, err := launcher.waitLine("squatting ", releaseLineTimeout); err != nil {
		return err
	}
	if run.squatterInc, err = startNS(run.pid); err != nil {
		return err
	}
	if err := keeper.Stdin.Close(); err != nil {
		return fmt.Errorf("release the keeper: %w", err)
	}
	if err := keeper.proc.wait(runTimeout); err != nil {
		return err
	}
	if err := tracer.wait(runTimeout); err != nil {
		return err
	}
	run.squatterLived = target.HostProcFS().Alive(uint32(run.pid), run.squatterInc)
	if err := stdin.Close(); err != nil {
		return fmt.Errorf("release the squatter: %w", err)
	}
	return launcher.wait(runTimeout)
}

// startLauncher starts the untraced `edgepeer pid-reuse` onto pid and
// returns once it reported the reuse, with the pipe to its stdin (which
// its squatter child inherited and holds the PID on).
func startLauncher(h *Harness, peer string, pid int) (*process, io.WriteCloser, error) {
	cmd := exec.Command(peer, "pid-reuse", "-pid", strconv.Itoa(pid), "-dir", h.Tmp)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("launcher stdin: %w", err)
	}
	launcher, err := startProcess("edgepeer pid-reuse", cmd)
	if err != nil {
		return nil, nil, err
	}
	h.procs = append(h.procs, launcher)
	line, err := launcher.waitLine("reused ", releaseLineTimeout)
	if err != nil {
		return nil, nil, err
	}
	if got := strings.TrimPrefix(line, "reused pid="); got != strconv.Itoa(pid) {
		return nil, nil, fmt.Errorf("launcher reused pid %s, want %d", got, pid)
	}
	return launcher, stdin, nil
}

// startNS reads pid's incarnation as the tracer does (/proc start time).
func startNS(pid int) (uint64, error) {
	st, err := target.HostProcFS().Stat(uint32(pid))
	if err != nil {
		return 0, fmt.Errorf("stat pid %d: %w", pid, err)
	}
	return st.StartNS, nil
}

// assertTargetKept: the target's bracket and file write are in the trace,
// and every event of its PID carries the target's incarnation — the
// squatter's start never leaked onto the old process's events.
func assertTargetKept(t *testing.T, run reuseRun) {
	t.Helper()
	var begin, end, wrote bool
	for _, e := range run.Trace.Events {
		if int(e.PID) != run.pid {
			continue
		}
		if e.PIDInc != run.targetInc {
			t.Fatalf("event %d (%s) of pid %d has incarnation %d, want the target's %d",
				e.Seq, e.Event, run.pid, e.PIDInc, run.targetInc)
		}
		begin = begin || isMarker(e, reuseScenario+"/begin")
		end = end || isMarker(e, reuseScenario+"/end")
		wrote = wrote || (e.Event == schema.KindOpenat && pathBase(e) == targetFile)
	}
	if !begin || !end || !wrote {
		t.Fatalf("target pid %d: begin %v, end %v, %s opened %v; want all", run.pid, begin, end, targetFile, wrote)
	}
}

// assertSquatterUntraced: nothing of the squatter is in the trace — no
// event of its incarnation, none of its bracket, no open of its file —
// while the keeper, traced all along, is (a run tracing nothing would
// pass the rest vacuously).
func assertSquatterUntraced(t *testing.T, run reuseRun) {
	t.Helper()
	keeperSeen := false
	for _, e := range run.Trace.Events {
		if int(e.PID) == run.pid && e.PIDInc == run.squatterInc {
			t.Fatalf("event %d (%s) of the squatter (pid %d@%d)", e.Seq, e.Event, run.pid, run.squatterInc)
		}
		if e.Event == schema.KindMarker && strings.HasPrefix(*e.Args.Marker, squatterScenario+"/") {
			t.Fatalf("squatter marker %q in the trace (seq %d, pid %d)", *e.Args.Marker, e.Seq, e.PID)
		}
		if pathBase(e) == squatterFile {
			t.Fatalf("event %d (%s of pid %d) touches the squatter's %s", e.Seq, e.Event, e.PID, squatterFile)
		}
		keeperSeen = keeperSeen || isMarker(e, keeperScenario+"/end")
	}
	if !keeperSeen {
		t.Fatal("the keeper's end marker is missing: the session did not trace it to the end")
	}
}

// assertOneNodeForPID: the graph has exactly one process node on the
// reused PID, the target's incarnation.
func assertOneNodeForPID(t *testing.T, run reuseRun) {
	t.Helper()
	want := schema.ProcessNodeID(run.Trace.Events[0].HostID, uint32(run.pid), run.targetInc)
	suffix := "/" + strconv.Itoa(run.pid) + "@"
	var nodes []string
	for _, n := range run.Trace.Graph.Nodes {
		if strings.Contains(n.ID, suffix) {
			nodes = append(nodes, n.ID)
		}
	}
	if len(nodes) != 1 || nodes[0] != want {
		t.Fatalf("process nodes on pid %d: %v, want only %s", run.pid, nodes, want)
	}
}

// isMarker reports whether e is the marker named name.
func isMarker(e schema.Event, name string) bool {
	return e.Event == schema.KindMarker && e.Args.Marker != nil && *e.Args.Marker == name
}

// pathBase is the base name of e's path argument, "" without one.
func pathBase(e schema.Event) string {
	if e.Args.Path == nil {
		return ""
	}
	return filepath.Base(*e.Args.Path)
}

package integrationtests

// A -listen-port session follows its port across a restart (PLAN.md
// decision #23, task mh4): every host expand -apply enrolls is bound by
// port, so a session that ended at the app's first restart lost the host.
// The app is workload/edgepeer's port-owner: the test kills the first
// incarnation, starts the second on the same port, and the session must
// adopt it, trace its work, and end only once no new owner appeared
// within its grace.

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"iormesh/internal/schema"
)

const (
	// portFollowScenario is port-owner's bracket.
	portFollowScenario = "port-follow"
	// portFollowGrace is the session's -listen-grace: long enough for the
	// second incarnation to start and bind after the first is killed, short
	// enough for the session to end by itself soon after the second exits.
	portFollowGrace = "5s"
	// portFollowAdopt bounds the wait for the session to adopt the second
	// incarnation: a live check, then a resolve at most a second later.
	portFollowAdopt = 10 * time.Second
)

// portFollowRun is one traced restart and the two incarnations' PIDs.
type portFollowRun struct {
	tracedRun
	port          int
	first, second int
}

// TestListenPortFollowsRestart: the first port-owner, the port's owner
// when the session starts, brackets file work and is killed; a second
// port-owner binds the same port. The session adopts it (a stderr line,
// an attach of its own incarnation), traces its bracketed work, and once
// it exits and nothing takes the port within the grace, ends
// targets_exited saying so. Nothing but the two incarnations is traced.
func TestListenPortFollowsRestart(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	peer := absPath(t, edgePeerDefault)
	h.ListenGrace = portFollowGrace
	run := judge(t, func() (portFollowRun, error) { return tracePortFollow(t, h, peer) })
	assertEndReason(t, run.Trace.Events, schema.EndTargetsExited)
	for _, want := range []string{
		fmt.Sprintf("port %d: following new owner pid %d", run.port, run.second),
		fmt.Sprintf("port %d: no new owner within %s", run.port, portFollowGrace),
	} {
		if !strings.Contains(run.Logged, want) {
			t.Fatalf("tracer stderr lacks %q:\n%s", want, run.Logged)
		}
	}
	assertOnlyTracedPIDs(t, run.Trace.Events, run.pids)
	assertTwoAttaches(t, run)
	for _, pid := range []int{run.first, run.second} {
		assertOwnerWorked(t, run, pid)
	}
}

// tracePortFollow is one attempt of the choreography; each attempt takes
// a fresh port.
func tracePortFollow(t *testing.T, h *Harness, peer string) (portFollowRun, error) {
	run := portFollowRun{}
	var err error
	if run.port, err = strconv.Atoi(freePort(t)); err != nil {
		return run, err
	}
	h.ListenPort = run.port
	port := strconv.Itoa(run.port)
	first, err := h.StartHeldHelper(peer, "port-owner", "-port", port, "-dir", h.Tmp)
	if err != nil {
		return run, err
	}
	run.first = first.PID
	h.runs++
	run.Dir = filepath.Join(h.Dir, fmt.Sprintf("trace-%d", h.runs))
	tracer, err := h.startTracer([]*Workload{first}, run.Dir)
	if err != nil {
		return run, err
	}
	if err := restartOwner(h, peer, port, first, tracer, &run); err != nil {
		return run, err
	}
	if err := tracer.wait(runTimeout); err != nil {
		return run, err
	}
	run.pids = []int{run.first, run.second}
	run.Logged = tracer.stderr.String()
	if run.Loss, err = ParseKernelLoss(run.Logged); err != nil {
		return run, err
	}
	run.Trace, err = ReadTrace(run.Dir)
	return run, err
}

// restartOwner runs the restart: the first owner works and is killed (no
// clean shutdown, as a crash), the second binds the port; once the
// tracer says it follows the second, the second works and exits.
func restartOwner(h *Harness, peer, port string, first *Workload, tracer *process, run *portFollowRun) error {
	if err := release([]*Workload{first}); err != nil {
		return err
	}
	if _, err := first.proc.waitLine("worked ", releaseLineTimeout); err != nil {
		return err
	}
	first.proc.kill()
	second, err := h.StartHeldHelper(peer, "port-owner", "-port", port, "-dir", h.Tmp)
	if err != nil {
		return err
	}
	run.second = second.PID
	if err := tracer.waitStderr(fmt.Sprintf("following new owner pid %d", second.PID), portFollowAdopt); err != nil {
		return err
	}
	if err := release([]*Workload{second}); err != nil {
		return err
	}
	if _, err := second.proc.waitLine("worked ", releaseLineTimeout); err != nil {
		return err
	}
	if err := second.Stdin.Close(); err != nil {
		return fmt.Errorf("release the second owner: %w", err)
	}
	return second.proc.wait(runTimeout)
}

// assertTwoAttaches: exactly one attach event per incarnation, on two
// different process nodes.
func assertTwoAttaches(t *testing.T, run portFollowRun) {
	t.Helper()
	var got []string
	for _, e := range run.Trace.Events {
		if e.Event == schema.KindAttach {
			got = append(got, schema.ProcessNodeID(e.HostID, e.PID, e.PIDInc))
		}
	}
	if len(got) != 2 || got[0] == got[1] ||
		!strings.Contains(got[0], fmt.Sprintf("/%d@", run.first)) ||
		!strings.Contains(got[1], fmt.Sprintf("/%d@", run.second)) {
		t.Fatalf("attach events of %v, want one for pid %d, then one for pid %d", got, run.first, run.second)
	}
}

// assertOwnerWorked: pid's bracket and its file's open are in the trace.
func assertOwnerWorked(t *testing.T, run portFollowRun, pid int) {
	t.Helper()
	file := fmt.Sprintf("port-owner-%d.dat", pid)
	var begin, end, wrote bool
	for _, e := range run.Trace.Events {
		if int(e.PID) != pid {
			continue
		}
		begin = begin || isMarker(e, portFollowScenario+"/begin")
		end = end || isMarker(e, portFollowScenario+"/end")
		wrote = wrote || (e.Event == schema.KindOpenat && pathBase(e) == file)
	}
	if !begin || !end || !wrote {
		t.Fatalf("owner pid %d: begin %v, end %v, %s opened %v; want all", pid, begin, end, file, wrote)
	}
}

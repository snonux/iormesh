package integrationtests

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"iormesh/internal/agentspool"
	"iormesh/internal/normalize"
	"iormesh/internal/schema"
)

const (
	// jep451Warning opens the four lines a JDK 21+ JVM prints on its
	// stderr when an agent is loaded into it dynamically (JEP 451).
	jep451Warning = "WARNING: A Java agent has been loaded dynamically"
	// attachJDKsEnv lists extra JAVA_HOMEs (colon-separated) the
	// dynamic-loading checks also run on, besides the java on PATH: how
	// 21 and 27-ea were checked (the workload needs 21 or newer).
	attachJDKsEnv = "IORMESH_ATTACH_JDKS"
	// awaitingAttach is live-attach's stdout line once its first half is
	// done (LiveAttach.AWAITING).
	awaitingAttach = "awaiting attach"
	// nobody is the uid and gid the other-user case runs its JVM as.
	nobody = 65534
)

// liveAttachRun is a judged live-attach run with what its choreography
// learned: the workload and the boot-clock time just before the attach.
type liveAttachRun struct {
	tracedRun
	w        *Workload
	attachNS uint64
}

// TestJavaLiveAttach is decision #2's live-attach path end to end (task
// ge4): a JVM started without -javaagent does live-attach's first half
// under the tracer, `iormesh attach` loads the agent into it mid-run, and
// once the tracer has probed the JVM's natives the second half runs. The
// golden declares the first half none and the rest java; beyond the
// bracket, no event of the JVM before the attach is attributed (a live
// agent cannot have seen it), and the JVM printed JEP 451's warning.
func TestJavaLiveAttach(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	h.AttachAgent = trustedAgentJar(t)
	h.Spool = filepath.Join(h.Dir, "spool")
	run := judge(t, func() (liveAttachRun, error) { return traceLiveAttach(h) })
	assertOnlyTracedPIDs(t, run.Trace.Events, run.pids)
	assertJavaConsistent(t, run.Trace)
	assertAttachSplit(t, run.Trace.Events, uint32(run.w.PID), run.attachNS)
	if !strings.Contains(run.w.proc.stderr.String(), jep451Warning) {
		t.Errorf("no JEP 451 warning on the JVM's stderr:\n%s", run.w.proc.stderr.String())
	}

	got := normalized(t, h, run.tracedRun, "live-attach")
	want := expectation(t, "live-attach")
	assertGolden(t, got, want)
	t.Run("wrong expectations are caught", func(t *testing.T) {
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			"before the attach declared java": func(x *normalize.Expectation) {
				x.Events[0].Attribution = schema.AttributionJava
				x.Events[0].Java = &normalize.ExpectedJava{Thread: "main",
					Method: "iormesh.workload.scenarios.LiveAttach$Unattached#write"}
			},
			"after the attach declared none": func(x *normalize.Expectation) {
				x.Events[3].Attribution, x.Events[3].Java = schema.AttributionNone, nil
			},
		})
	})
}

// traceLiveAttach is one attempt of TestJavaLiveAttach: the workload waits
// at its attach gate after the first half (Trace's release waits for that
// line), and the Midway step attaches, waits for the tracer to report the
// JVM joined, and opens the gate.
func traceLiveAttach(h *Harness) (liveAttachRun, error) {
	var r liveAttachRun
	gate := filepath.Join(h.Dir, fmt.Sprintf("attach-gate-%d", h.runs))
	w, err := h.StartWorkload("live-attach", "--attach-gate", gate)
	if err != nil {
		return r, err
	}
	w.ReleasedWhen = awaitingAttach
	r.w, r.pids = w, []int{w.PID}
	h.Midway = func(tracer *process) error {
		r.attachNS = bootNow()
		if _, err := h.Attach(w); err != nil {
			return err
		}
		joined := fmt.Sprintf("java agent of pid %d joined", w.PID)
		if err := tracer.waitStderr(joined, attachTimeout); err != nil {
			return err
		}
		return os.WriteFile(gate, nil, 0o600)
	}
	r.Run, err = h.Trace(w)
	return r, err
}

// assertAttachSplit: no event of pid that ended before the attach began
// is attributed — the agent was not there to bracket it, and the tracer
// must not guess — and there are such events, so the check ran.
func assertAttachSplit(t *testing.T, events []schema.Event, pid uint32, attachNS uint64) {
	t.Helper()
	before := 0
	for _, e := range events {
		if e.PID != pid || e.BootNS+e.LatencyNS >= attachNS {
			continue
		}
		before++
		if e.Attribution != schema.AttributionNone {
			t.Errorf("event seq %d (%s) ended before the attach but is attribution %s",
				e.Seq, e.Event, e.Attribution)
		}
	}
	if before == 0 {
		t.Error("no event of the JVM before the attach: the split was not exercised")
	}
}

// TestJavaLiveAttachAtStart: `run -agent` loads the agent into the JVM it
// was started on before its ready file appears, so method-flow, released
// at ready, is attributed exactly as under -javaagent — and the JVM
// printed JEP 451's warning.
func TestJavaLiveAttachAtStart(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	h.AttachAgent = trustedAgentJar(t)
	h.AgentAtStart = true
	h.Spool = filepath.Join(h.Dir, "spool")
	var w *Workload
	run := traceJudged(t, h, func(h *Harness) ([]*Workload, error) {
		ws, err := startScenario("method-flow")(h)
		if err == nil {
			w = ws[0]
		}
		return ws, err
	})
	if !strings.Contains(run.Logged, fmt.Sprintf("agent loaded into pid %d", w.PID)) {
		t.Errorf("the tracer does not report the load: %s", run.Logged)
	}
	if !strings.Contains(w.proc.stderr.String(), jep451Warning) {
		t.Errorf("no JEP 451 warning on the JVM's stderr:\n%s", w.proc.stderr.String())
	}
	assertJavaConsistent(t, run.Trace)
	assertGolden(t, normalized(t, h, run, "method-flow"), expectation(t, "method-flow"))
}

// TestJavaAttachDynamicLoading is decision #2's flag matrix, without a
// tracer (the load is what is judged): on each JDK a live attach works
// and warns by default, works silently with -XX:+EnableDynamicAgentLoading,
// and is refused with -XX:-EnableDynamicAgentLoading — the default of a
// later release — with the JVM running on unharmed. A root attacher also
// reaches a JVM of another uid.
func TestJavaAttachDynamicLoading(t *testing.T) {
	enableParallelIfRequested(t)
	javas := []string{""}
	for _, home := range strings.Split(os.Getenv(attachJDKsEnv), ":") {
		if home != "" {
			javas = append(javas, filepath.Join(home, "bin", "java"))
		}
	}
	for _, java := range javas {
		name := "path"
		if java != "" {
			name = filepath.Base(filepath.Dir(filepath.Dir(java)))
		}
		t.Run(name+"/default", func(t *testing.T) { checkAttach(t, java, "", true) })
		t.Run(name+"/opted in", func(t *testing.T) {
			checkAttach(t, java, "-XX:+EnableDynamicAgentLoading", true)
		})
		t.Run(name+"/disabled", func(t *testing.T) {
			checkAttach(t, java, "-XX:-EnableDynamicAgentLoading", false)
		})
	}
	t.Run("another uid", checkAttachOtherUID)
}

// checkAttach runs live-attach on java with flag, attaches between its
// halves and judges the load (wantLoad), the warning (only a default
// JVM's) and the JVM's clean exit either way.
func checkAttach(t *testing.T, java, flag string, wantLoad bool) {
	h := newAttachHarness(t, t.TempDir(), trustedAgentJar(t))
	h.Java = java
	if flag != "" {
		h.JVMFlags = []string{flag}
	}
	stderr, _ := attachBetweenHalves(t, h, wantLoad)
	warned := strings.Contains(stderr, jep451Warning)
	if wantWarn := wantLoad && flag == ""; warned != wantWarn {
		t.Errorf("JEP 451 warning on the JVM's stderr: %v, want %v:\n%s", warned, wantWarn, stderr)
	}
}

// checkAttachOtherUID: the JVM runs as nobody, the attacher as root (the
// test binary). Both jars are copied into a directory the user nobody
// can read; its sticky bit keeps the tracer's agent-jar check satisfied
// (nobody may create files there, not replace root's).
func checkAttachOtherUID(t *testing.T) {
	requireRootForProbe(t) // the attacher must be root to reach another uid
	shared, err := os.MkdirTemp("/tmp", "iormesh-attach-uid-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shared) })
	if err := os.Chmod(shared, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	agent := copyReadable(t, absPath(t, agentJarDefault), shared)
	h := newAttachHarness(t, shared, agent)
	h.WorkloadJar = copyReadable(t, h.WorkloadJar, shared)
	h.Dir, h.Tmp = shared, shared
	h.Credential = &syscall.Credential{Uid: nobody, Gid: nobody}
	stderr, ringUID := attachBetweenHalves(t, h, true)
	if !strings.Contains(stderr, jep451Warning) {
		t.Errorf("no JEP 451 warning on the JVM's stderr:\n%s", stderr)
	}
	// The agent ran as the JVM's user: its ring was nobody's.
	if ringUID != nobody {
		t.Errorf("ring uid %d; want %d", ringUID, nobody)
	}
}

// newAttachHarness is a harness for a tracer-less attach: the spool is
// created in dir as `iormesh run -spool` creates it.
func newAttachHarness(t *testing.T, dir, agent string) *Harness {
	t.Helper()
	h := newHarness(t)
	h.AttachAgent = agent
	h.Spool = filepath.Join(dir, "spool")
	if err := agentspool.MakeSpool(h.Spool); err != nil {
		t.Fatal(err)
	}
	return h
}

// attachBetweenHalves runs live-attach, attaches at its gate, opens the
// gate and returns the JVM's stderr once it exited cleanly, and the uid
// owning its ring while it ran (unknown: MaxUint32). wantLoad says
// whether the attach must succeed (and leave a ring while the JVM runs,
// which its shutdown hook removes, task 2j4) or be refused for dynamic
// loading being disabled (and leave none).
func attachBetweenHalves(t *testing.T, h *Harness, wantLoad bool) (string, uint32) {
	t.Helper()
	gate := filepath.Join(h.Dir, "attach-gate")
	w, err := h.StartWorkload("live-attach", "--attach-gate", gate)
	if err != nil {
		t.Fatal(err)
	}
	w.ReleasedWhen = awaitingAttach
	if err := release([]*Workload{w}); err != nil {
		t.Fatal(err)
	}
	out, err := h.Attach(w)
	ring := filepath.Join(h.Spool, fmt.Sprintf("%d.ring", w.PID))
	var st unix.Stat_t
	ringErr := unix.Stat(ring, &st)
	ringUID := uint32(math.MaxUint32)
	if ringErr == nil {
		ringUID = st.Uid
	}
	switch {
	case wantLoad && err != nil:
		t.Errorf("attach failed: %v", err)
	case wantLoad && ringErr != nil:
		t.Errorf("attach succeeded but left no ring: %v (%s)", ringErr, out)
	case !wantLoad && (err == nil || !strings.Contains(out, "dynamic agent loading is disabled")):
		t.Errorf("attach to a JVM without dynamic loading: err %v, stderr %q", err, out)
	case !wantLoad && ringErr == nil:
		t.Errorf("a refused attach left a ring %s", ring)
	}
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.proc.wait(runTimeout); err != nil {
		t.Fatalf("the JVM did not finish cleanly after the attach: %v", err)
	}
	if _, err := os.Lstat(ring); wantLoad && !os.IsNotExist(err) {
		t.Errorf("the JVM's shutdown hook left its ring %s: %v", ring, err)
	}
	return w.proc.stderr.String(), ringUID
}

// trustedAgentJar copies iormesh-agent.jar into a fresh root-owned
// directory under /tmp: the tracer loads only a jar that nobody but root
// or the caller can change, up its whole path (cli.trustedAgent), and the
// build's copy lies in the developer's checkout.
func trustedAgentJar(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "iormesh-agent-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return copyReadable(t, absPath(t, agentJarDefault), dir)
}

// copyReadable copies src into dir, readable by everyone.
func copyReadable(t *testing.T, src, dir string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, filepath.Base(src))
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dst
}

// bootNow is CLOCK_BOOTTIME, the clock of events' boot_ns.
func bootNow() uint64 {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err != nil {
		panic(err)
	}
	return uint64(ts.Nano())
}

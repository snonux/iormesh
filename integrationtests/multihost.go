package integrationtests

// Emulated hosts for M4's multi-host scenarios (task kb4; PLAN.md "Testing
// strategy": "Emulated hosts share a kernel — compensate"). One machine
// plays several hosts: each host is a network namespace for its
// processes, and its tracer runs in a mount and UTS namespace of its own
// with a private /etc/machine-id bind-mounted over the real one, so the
// tracer stamps a host id of its own on every event (internal/cli
// machineID) — exactly what tells two real hosts apart in a merge.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// netnsDir is where `ip netns add` pins named network namespaces.
const netnsDir = "/run/netns"

// hostShell becomes an emulated host before it execs the tracer: the
// machine id ($1) over /etc/machine-id, the hostname ($2), the host's
// network namespace ($3). It runs inside `unshare --mount --uts`, so none
// of it leaks to the machine. nsenter switches the network namespace
// only: `ip netns exec` would also remount /sys and lose tracefs, which
// the tracer's probes need.
const hostShell = `mount --bind "$1" /etc/machine-id && hostname "$2" && ` +
	`ns="$3" && shift 3 && exec nsenter --net="$ns" -- "$@"`

// testClockSkewEnv is the tracer's test-only wall-clock skew
// (internal/cli clockskew.go): the emulated hosts share one kernel clock,
// so a host's clock is made to differ by skewing what its tracer stamps.
const testClockSkewEnv = "IORMESH_TEST_CLOCK_SKEW"

// Host is one emulated host of a multi-host run: its name (hostname and
// trace directory), the project its tracer records, the network
// namespace its processes live in, and the workloads its tracer targets
// by PID. ClockSkew, if not 0, is how far this host's wall clock is off:
// its tracer adds it to every wall_ns (boot_ns, the machine's one boot
// clock, stays true).
type Host struct {
	Name, Project, Netns string
	Workloads            []*Workload
	ClockSkew            time.Duration
}

// StartWorkloadIn is StartWorkload for a process of an emulated host: the
// JVM runs in network namespace netns (nsenter execs it, so the PID it
// prints is its own).
func (h *Harness) StartWorkloadIn(netns, scenario string, args ...string) (*Workload, error) {
	if h.Credential != nil {
		// nsenter would exec the JVM as root, not as the credential's
		// user: a test needing both would silently test the wrong thing.
		return nil, fmt.Errorf("workload %s: a Credential is not supported on an emulated host",
			scenario)
	}
	cmd, err := h.workloadCmd(h.Tmp, scenario, args)
	if err != nil {
		return nil, err
	}
	argv := append([]string{"--net=" + filepath.Join(netnsDir, netns), "--", cmd.Path},
		cmd.Args[1:]...)
	in := exec.Command("nsenter", argv...)
	in.SysProcAttr, in.Dir = cmd.SysProcAttr, cmd.Dir
	return h.startGated(scenario, in, false)
}

// TraceHosts traces each host with its own tracer, as each real host runs
// its own: every tracer attaches first, then the workloads are released
// in the given order across hosts (a server before its client), and once
// all exited each host's trace is read. The runs come back in hosts'
// order.
func (h *Harness) TraceHosts(order []*Workload, hosts ...Host) ([]Run, error) {
	h.runs++
	tracers := make([]*process, len(hosts))
	runs := make([]Run, len(hosts))
	for i, host := range hosts {
		runs[i].Dir = filepath.Join(h.Dir, fmt.Sprintf("trace-%d-%s", h.runs, host.Name))
		t, err := h.startHostTracer(host, runs[i].Dir)
		if err != nil {
			return nil, err
		}
		tracers[i] = t
	}
	if err := release(order); err != nil {
		return nil, err
	}
	for _, w := range order {
		if err := w.proc.wait(runTimeout); err != nil {
			return nil, skipped(w, err)
		}
	}
	for i := range hosts {
		if err := finishRun(tracers[i], &runs[i]); err != nil {
			return nil, fmt.Errorf("host %s: %w", hosts[i].Name, err)
		}
	}
	return runs, nil
}

// startHostTracer starts host's `iormesh run` as that host — its own
// machine id, hostname and network namespace — and returns once it is
// attached.
func (h *Harness) startHostTracer(host Host, out string) (*process, error) {
	// A host tracer is the plain default tracer: the opt-in families,
	// native stacks, segments and the Java agent of Harness are not passed
	// on, so a test setting them would check what never ran.
	if h.Families != "" || h.NativeStacks || h.Segments || h.JavaAgent != "" ||
		h.AttachAgent != "" || h.Pattern != "" {
		return nil, fmt.Errorf("host %s: emulated hosts take no tracer options yet", host.Name)
	}
	mid := filepath.Join(h.Dir, host.Name+".machine-id")
	if err := writeMachineID(mid); err != nil {
		return nil, err
	}
	ready := out + ".ready"
	args := []string{"--mount", "--uts", "--propagation", "private", "--",
		"sh", "-c", hostShell, "sh", mid, host.Name, filepath.Join(netnsDir, host.Netns),
		h.TracerBinary, "run", "-pid", workloadPIDs(host.Workloads), "-project", host.Project,
		"-out", out, "-duration", tracerDuration, "-ready", ready}
	cmd := exec.Command("unshare", args...)
	cmd.Env = hostTracerEnv(h.BpfObject, host.ClockSkew)
	tracer, err := startProcess("iormesh run on "+host.Name, cmd)
	if err != nil {
		return nil, err
	}
	h.procs = append(h.procs, tracer)
	return tracer, waitReady(tracer, ready)
}

// hostTracerEnv is a host tracer's environment: the test's own, plus the
// BPF object override and the host's clock skew when set.
func hostTracerEnv(bpfObject string, skew time.Duration) []string {
	env := os.Environ()
	if bpfObject != "" {
		env = append(env, bpfObjectOverrideEnv+"="+bpfObject)
	}
	if skew != 0 {
		env = append(env, testClockSkewEnv+"="+skew.String())
	}
	return env
}

// finishRun waits for a tracer whose targets exited and reads its loss
// figures and trace into run.
func finishRun(tracer *process, run *Run) error {
	waitErr := tracer.wait(runTimeout)
	run.Logged = tracer.stderr.String()
	if waitErr != nil {
		return waitErr
	}
	var err error
	if run.Loss, err = ParseKernelLoss(run.Logged); err != nil {
		return err
	}
	run.Trace, err = ReadTrace(run.Dir)
	return err
}

// writeMachineID writes a fresh random machine id (32 hex digits and a
// newline, machine-id(5)'s format) to path: a new host every run.
func writeMachineID(path string) error {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(hex.EncodeToString(b)+"\n"), 0o644)
}

// workloadPIDs is the tracer's -pid list for ws.
func workloadPIDs(ws []*Workload) string {
	pids := make([]string, len(ws))
	for i, w := range ws {
		pids[i] = fmt.Sprint(w.PID)
	}
	return strings.Join(pids, ",")
}

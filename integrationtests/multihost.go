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
// clock, stays true). Spool is the host's own Java agent spool, required
// when Harness.JavaAgent is set (task 1f4): its JVMs write their rings
// there and only its tracer drains them, as on a real host — two tracers
// sharing one spool would each drain the other host's rings.
//
// ListenPort, if not 0, binds the host's tracer by listening port
// instead of by its Workloads' PIDs (-listen-port, the target spec mesh
// expansion deploys with, PLAN.md M6): such a tracer is started once the
// servers listen — a port nobody listens on is no target — and before
// the clients are released. Workloads then only name the PIDs its trace
// may hold.
type Host struct {
	Name, Project, Netns, Spool string
	Workloads                   []*Workload
	ClockSkew                   time.Duration
	ListenPort                  uint16
}

// StartWorkloadIn is StartWorkload for a process of an emulated host: the
// JVM runs in network namespace netns (nsenter execs it, so the PID it
// prints is its own). With Harness.JavaAgent its agent writes into the
// harness's Spool; StartWorkloadOn gives it its host's.
func (h *Harness) StartWorkloadIn(netns, scenario string, args ...string) (*Workload, error) {
	return h.StartWorkloadOn(Host{Netns: netns, Spool: h.Spool}, scenario, args...)
}

// StartWorkloadOn is StartWorkloadIn on host: its network namespace and,
// with Harness.JavaAgent, its own spool.
func (h *Harness) StartWorkloadOn(host Host, scenario string, args ...string) (*Workload, error) {
	if h.Credential != nil {
		// nsenter would exec the JVM as root, not as the credential's
		// user: a test needing both would silently test the wrong thing.
		return nil, fmt.Errorf("workload %s: a Credential is not supported on an emulated host",
			scenario)
	}
	cmd, err := h.workloadCmd(h.Tmp, host.Spool, scenario, args)
	if err != nil {
		return nil, err
	}
	argv := append([]string{"--net=" + filepath.Join(netnsDir, host.Netns), "--", cmd.Path},
		cmd.Args[1:]...)
	in := exec.Command("nsenter", argv...)
	in.SysProcAttr, in.Dir = cmd.SysProcAttr, cmd.Dir
	return h.startGated(scenario, in, false)
}

// TraceHosts traces each host with its own tracer, as each real host runs
// its own: every tracer attaches first, then the workloads are released
// in the given order across hosts (a server before its client), and once
// all exited each host's trace is read. The runs come back in hosts'
// order. A host with a ListenPort is the exception: its tracer attaches
// after the servers (the workloads up to the last with ReleasedWhen)
// printed their listening line, before the rest are released. A
// workload of no host in hosts runs untraced.
func (h *Harness) TraceHosts(order []*Workload, hosts ...Host) ([]Run, error) {
	h.runs++
	tracers := make([]*process, len(hosts))
	runs := make([]Run, len(hosts))
	start := func(late bool) error {
		for i, host := range hosts {
			if (host.ListenPort != 0) != late {
				continue
			}
			runs[i].Dir = filepath.Join(h.Dir, fmt.Sprintf("trace-%d-%s", h.runs, host.Name))
			t, err := h.startHostTracer(host, runs[i].Dir)
			if err != nil {
				return err
			}
			tracers[i] = t
		}
		return nil
	}
	servers := serversEnd(order)
	if err := start(false); err != nil {
		return nil, err
	}
	if err := release(order[:servers]); err != nil {
		return nil, err
	}
	if err := start(true); err != nil {
		return nil, err
	}
	if err := release(order[servers:]); err != nil {
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

// serversEnd is the index after the last workload with ReleasedWhen: the
// servers TraceHosts releases before attaching listening-port tracers.
func serversEnd(order []*Workload) int {
	end := 0
	for i, w := range order {
		if w.ReleasedWhen != "" {
			end = i + 1
		}
	}
	return end
}

// hostTarget is a host tracer's target spec: its listening port, else
// its workloads' PIDs. A port session ends as soon as its owner exited
// (-listen-grace 0): no host app restarts here, and the default grace
// (task mh4) would keep the tracer past finishRun's timeout.
func hostTarget(host Host) []string {
	if host.ListenPort != 0 {
		return []string{"-listen-port", fmt.Sprint(host.ListenPort), "-listen-grace", "0"}
	}
	return []string{"-pid", workloadPIDs(host.Workloads)}
}

// startHostTracer starts host's `iormesh run` as that host — its own
// machine id, hostname and network namespace — and returns once it is
// attached.
func (h *Harness) startHostTracer(host Host, out string) (*process, error) {
	// A host tracer is the default tracer plus, with Harness.JavaAgent,
	// the host's own spool: the opt-in families, native stacks, a live
	// attach and a pattern are not passed on, so a test setting them
	// would check what never ran.
	if h.Families != "" || h.NativeStacks || h.AttachAgent != "" || h.Pattern != "" {
		return nil, fmt.Errorf("host %s: emulated hosts take no tracer options but -spool yet",
			host.Name)
	}
	if h.JavaAgent != "" && host.Spool == "" {
		return nil, fmt.Errorf("host %s: the Java agent needs the host's own Spool", host.Name)
	}
	mid := filepath.Join(h.Dir, host.Name+".machine-id")
	if err := writeMachineID(mid); err != nil {
		return nil, err
	}
	ready := out + ".ready"
	args := append([]string{"--mount", "--uts", "--propagation", "private", "--",
		"sh", "-c", hostShell, "sh", mid, host.Name, filepath.Join(netnsDir, host.Netns),
		h.TracerBinary, "run"}, hostTarget(host)...)
	args = append(args, "-project", host.Project,
		"-out", out, "-duration", tracerDuration, "-ready", ready,
		// Segments too, as startTracer writes them (task ze4).
		"-trace-dir", out+segmentsSuffix, "-disk-limit", "0", "-disk-min-free", "0")
	if h.JavaAgent != "" {
		args = append(args, "-spool", host.Spool)
	}
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
// newline, machine-id(5)'s format) to path, unless the harness gave the
// host one already: a host keeps its id across a harness's runs, as a
// real host keeps it across tracer sessions — mesh expansion (task tb4)
// traces the same hosts round after round and merges every round.
func writeMachineID(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
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

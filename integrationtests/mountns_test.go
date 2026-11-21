package integrationtests

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"iormesh/internal/schema"
)

// privateTmpWrap runs a JVM in a mount namespace of its own with an
// empty tmpfs at /tmp — an app container's view beside an in-pod tracer
// (M7) — and execs it in the same process, so the PID the JVM prints is
// the one the tracer targets.
var privateTmpWrap = []string{"unshare", "--mount", "--propagation", "private", "--",
	"sh", "-c", `mount -t tmpfs -o mode=1777 iormesh-jvm-tmp /tmp && exec "$@"`, "sh"}

// nobodyPrivateTmpWrap is privateTmpWrap with the JVM run as nobody
// (setpriv, after the mount only root may make), from / (the test's own
// working directory may be closed to that user).
var nobodyPrivateTmpWrap = []string{"unshare", "--mount", "--propagation", "private", "--",
	"sh", "-c", `mount -t tmpfs -o mode=1777 iormesh-jvm-tmp /tmp && cd / && ` +
		`exec setpriv --reuid=` + strconv.Itoa(nobody) + ` --regid=` + strconv.Itoa(nobody) +
		` --clear-groups "$@"`, "sh"}

// TestJavaAttachMountNS is decision #14's staging (M7 step 3, task cf4):
// http-plain's server (as nobody) and client (as root) each run in their
// own mount namespace with a private /tmp, so neither sees the tracer's
// agent jar or spool, and `run -agent` stages both inside each JVM's
// root. The Java join must still work end to end — both rings read, the
// client's requests carry their host, the golden holds as under
// -javaagent — with no ring in the tracer's own spool, and every stage
// removed from the JVMs' /tmp once the run ended.
func TestJavaAttachMountNS(t *testing.T) {
	enableParallelIfRequested(t)
	requireRootForProbe(t) // mount namespaces and a tmpfs
	h := newHarness(t)
	// The JVMs' /tmp is their own: the gate files, the workload jar and
	// the spool must lie elsewhere, and nobody must reach the first two.
	h.Dir, h.Tmp = dirOutsideTmp(t), dirOutsideTmp(t)
	for _, d := range []string{h.Dir, h.Tmp} {
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h.WorkloadJar = copyReadable(t, h.WorkloadJar, h.Tmp)
	h.AttachAgent = trustedAgentJar(t)
	h.AgentAtStart = true
	h.Spool = filepath.Join(h.Dir, "spool")
	var tmps []int
	run := traceJudged(t, h, func(h *Harness) ([]*Workload, error) {
		ws, err := startMixedUserPair(t, h)
		for _, w := range ws {
			tmps = append(tmps, holdJVMTmp(t, w.PID))
		}
		return ws, err
	})
	for _, pid := range run.pids {
		if want := fmt.Sprintf("agent loaded into pid %d;", pid); !strings.Contains(run.Logged, want) {
			t.Errorf("tracer log lacks %q:\n%s", want, run.Logged)
		}
	}
	if !strings.Contains(run.Logged, "java rings 2 (") || !strings.Contains(run.Logged, "(no connection 0)") {
		t.Errorf("summary does not report two rings and every request on its connection: %s", run.Logged)
	}
	assertOnlyTracedPIDs(t, run.Trace.Events, run.pids)
	assertJavaConsistent(t, run.Trace)
	assertHTTPHost(t, run.Trace.Events)
	assertGolden(t, normalized(t, h, run, "http-plain"), expectation(t, "http-plain"))
	if rings, _ := filepath.Glob(filepath.Join(h.Spool, "*.ring")); len(rings) != 0 {
		t.Errorf("rings in the tracer's own spool, not the stages: %v", rings)
	}
	for _, fd := range tmps {
		assertNoStage(t, fd)
	}
}

// startMixedUserPair starts http-plain's server as nobody and its client
// as root, each under its private-/tmp wrapper, and checks the server
// really runs as nobody (a non-root JVM's stage and ring).
func startMixedUserPair(t *testing.T, h *Harness) ([]*Workload, error) {
	port := freePort(t)
	h.JVMWrap = nobodyPrivateTmpWrap
	server, err := h.StartNoDir("http-plain", "--role", "server", "--port", port)
	if err != nil {
		return nil, err
	}
	server.ReleasedWhen = "listening "
	var st unix.Stat_t
	if err := unix.Stat(filepath.Join("/proc", strconv.Itoa(server.PID)), &st); err != nil || st.Uid != nobody {
		return nil, fmt.Errorf("server pid %d: uid %d, %v; want nobody", server.PID, st.Uid, err)
	}
	h.JVMWrap = privateTmpWrap
	client, err := h.StartNoDir("http-plain", "--role", "client", "--port", port)
	if err != nil {
		return nil, err
	}
	return []*Workload{server, client}, nil
}

// dirOutsideTmp is a fresh directory under /var/tmp, out of the private
// /tmp the JVMs get.
func dirOutsideTmp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/var/tmp", "iormesh-mntns-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// holdJVMTmp opens pid's /tmp and keeps it open past the JVM's exit, so
// the test can still look into the tmpfs once the namespace is gone. It
// must not be the tracer's /tmp: the namespace is what is under test.
func holdJVMTmp(t *testing.T, pid int) int {
	t.Helper()
	fd, err := unix.Open(filepath.Join("/proc", strconv.Itoa(pid), "root", "tmp"),
		unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("pid %d's /tmp: %v", pid, err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	var theirs, ours unix.Stat_t
	if err := unix.Fstat(fd, &theirs); err != nil {
		t.Fatal(err)
	}
	if err := unix.Stat("/tmp", &ours); err != nil {
		t.Fatal(err)
	}
	if theirs.Dev == ours.Dev && theirs.Ino == ours.Ino {
		t.Fatalf("pid %d shares the tracer's /tmp: no mount namespace of its own", pid)
	}
	return fd
}

// assertHTTPHost: the client's requests carry their host, which only
// the agent's ring can have told the tracer.
func assertHTTPHost(t *testing.T, events []schema.Event) {
	t.Helper()
	for _, e := range events {
		if e.Event == schema.KindHTTP && e.HTTP.Host != "" {
			return
		}
	}
	t.Error("no http event with a host: the staged agent's records were not joined")
}

// assertNoStage: the JVM's /tmp (held as fd) holds no iormesh-* stage
// any more — the session removed it — but the JVM's own files are still
// there (hsperfdata), so the listing did reach its /tmp.
func assertNoStage(t *testing.T, fd int) {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd/" + strconv.Itoa(fd))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		if strings.HasPrefix(e.Name(), "iormesh-") {
			t.Errorf("stage %s left in the JVM's /tmp", e.Name())
		}
	}
	if len(names) == 0 {
		t.Error("the JVM's /tmp is empty: not the JVM's (no hsperfdata)")
	}
}

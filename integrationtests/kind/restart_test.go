package kind

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestKindDebugRestart is task ah4's check on a real cluster (mage
// k8sDebugRestart): a debug tracer with the Java agent dies with its
// app container when that container's init is SIGKILLed and the kubelet
// restarts it; deploy then starts iormesh-2, which attaches (a rerun of
// deploy while it attaches waits past the predecessor's dead
// state.json, review N1), and collect fetches the segments both
// sessions published from the one emptyDir into the one pod-<uid>.
func TestKindDebugRestart(t *testing.T) {
	c := newCluster(t, "iormesh-ah4")
	c.prepare(t, false)
	reg := c.writeAgentRegistry(t)
	pod := c.pod(t, "server").Metadata.Name
	s1 := c.gatedSession(t, pod, "iormesh", func() string { return c.ctl(t, nil, "deploy", "-registry", reg, c.ref("server")) })
	time.Sleep(rotation) // s1 publishes a segment
	c.killAppInit(t, pod)
	c.waitTracerEnded(t, pod, "iormesh")
	if out, err := c.ctlTry(nil, "collect", "-registry", reg, "-store", c.store(), "-db", c.db()); err == nil ||
		!strings.Contains(out, "iormeshctl deploy "+c.ref("server")+" starts a fresh tracer") {
		t.Errorf("collect with the tracer dead: err %v, want the deploy advice:\n%s", err, out)
	}
	s2 := c.gatedSession(t, pod, "iormesh-2", func() string { return c.deployTwice(t, reg, pod) })
	if s2 == s1 {
		t.Fatalf("iormesh-2 reports session %s, the killed tracer's", s1)
	}
	time.Sleep(rotation) // s2 publishes a segment
	c.ctl(t, nil, "collect", "-registry", reg, "-store", c.store(), "-db", c.db())
	uid := c.pod(t, "server").Metadata.UID
	got := c.storeSessions(t, filepath.Join(c.store(), c.ns, "pod-"+uid))
	t.Logf("sessions in pod-%s: %v (killed tracer %s, iormesh-2 %s)", uid, got, s1, s2)
	for _, s := range []string{s1, s2} {
		if got[s] == 0 {
			t.Errorf("no segment of session %s collected (have %v)", s, got)
		}
	}
}

// writeAgentRegistry is the registry of the server pod alone, its debug
// tracer with the Java agent (agentFlags).
func (c *cluster) writeAgentRegistry(t *testing.T) string {
	t.Helper()
	text := fmt.Sprintf("k8s:\n  image: %s\n  clusters:\n    - name: kind\n      context: %s\n      namespace: %s\n"+
		"pods:\n  - cluster: kind\n    selector: app=server\n    container: app\n    projects:\n"+
		"      - target: {pattern: %q}\n        flags: [%s]\n", c.cfg.tracer, c.cfg.context, c.ns, pattern,
		quoteAll(agentFlags))
	path := filepath.Join(c.dir, "servers.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// gatedSession runs a gated file-basic JVM in pod's app container, then
// deploy (which must report tracer attached, with its session), waits
// until the agent joined that JVM, releases it and waits for its end
// marker. It returns the session deploy reported.
func (c *cluster) gatedSession(t *testing.T, pod, tracer string, deploy func() string) string {
	t.Helper()
	c.kubectl(t, "exec", pod, "--container=app", "--", "sh", "-c", "rm -rf /tmp/w /tmp/go && mkdir -p /tmp/w")
	jvm := c.start(t, "file-basic "+tracer, pod, []string{"IORMESH_WORKLOAD_STARTUP_FILE=/tmp/go"}, "java", "-jar",
		workloadJar, "file-basic", "--dir", "/tmp/w")
	jvm.waitFor(t, "\n", time.Minute) // the PID line: gated
	out := deploy()
	m := regexp.MustCompile(`tracer ` + regexp.QuoteMeta(tracer) + `[ ,].*attached, session ([0-9a-f]{8})`).
		FindStringSubmatch(out)
	if tracer == "iormesh" {
		m = regexp.MustCompile(`started tracer, .*attached, session ([0-9a-f]{8})`).FindStringSubmatch(out)
	}
	if m == nil {
		t.Fatalf("deploy did not report %s attached:\n%s", tracer, out)
	}
	c.waitContainerLog(t, pod, tracer, "joined", time.Minute)
	c.kubectl(t, "exec", pod, "--container=app", "--", "touch", "/tmp/go")
	jvm.wait(t, "file-basic")
	return m[1]
}

// deployTwice runs the deploy that replaces the killed tracer and,
// once iormesh-2 runs but before that deploy returned (it waits for the
// attach), a second deploy: N1's rerun, which must leave iormesh-2
// alone and report the same session rather than fail on the
// predecessor's dead state. It returns the first deploy's output.
func (c *cluster) deployTwice(t *testing.T, reg, pod string) string {
	t.Helper()
	type result struct {
		out string
		err error
		at  time.Time
	}
	first := make(chan result, 1)
	start := time.Now()
	go func() {
		out, err := c.ctlTry(nil, "deploy", "-registry", reg, c.ref("server"))
		first <- result{out, err, time.Now()}
	}()
	var rerun *result
	deadline := time.Now().Add(2 * time.Minute)
	for rerun == nil && time.Now().Before(deadline) {
		select {
		case r := <-first:
			first <- r
			t.Logf("N1: the replacing deploy returned (%s) before iormesh-2 was seen running; no rerun timed",
				r.at.Sub(start).Round(time.Millisecond))
			deadline = time.Now()
			continue
		default:
		}
		if c.ephemeralRunning(t, pod, "iormesh-2") {
			at := time.Now()
			out, err := c.ctlTry(nil, "deploy", "-registry", reg, c.ref("server"))
			rerun = &result{out, err, at}
			continue
		}
		time.Sleep(200 * time.Millisecond)
	}
	r := <-first
	if r.err != nil {
		t.Fatalf("replacing deploy: %v\n%s", r.err, r.out)
	}
	if !strings.Contains(r.out, "earlier tracer ended") || !strings.Contains(r.out, "started tracer iormesh-2") {
		t.Fatalf("replacing deploy did not start iormesh-2:\n%s", r.out)
	}
	t.Logf("replacing deploy (%s):\n%s", r.at.Sub(start).Round(time.Millisecond), r.out)
	if rerun != nil {
		t.Logf("N1 rerun started %s in, %s before the replacing deploy returned; err %v:\n%s",
			rerun.at.Sub(start).Round(time.Millisecond), r.at.Sub(rerun.at).Round(time.Millisecond), rerun.err,
			rerun.out)
		if rerun.err != nil || !strings.Contains(rerun.out, "tracer iormesh-2 already running the pinned image") {
			t.Errorf("N1: the rerun did not leave iormesh-2 alone attached: %v\n%s", rerun.err, rerun.out)
		}
	}
	return r.out
}

// ctlTry is ctl returning the error instead of failing the test (safe
// from a goroutine).
func (c *cluster) ctlTry(env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.cfg.ctl, args...)
	cmd.Env = append(append(os.Environ(), "KUBECONFIG="+c.cfg.kubeconfig), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ephemeralRunning reports whether pod's ephemeral container name runs.
func (c *cluster) ephemeralRunning(t *testing.T, pod, name string) bool {
	t.Helper()
	st, ok := c.ephemeralState(t, pod, name)
	return ok && st.Running != nil
}

// ephemeralState is the state of pod's ephemeral container name.
func (c *cluster) ephemeralState(t *testing.T, pod, name string) (stateJSON, bool) {
	t.Helper()
	var p podJSON
	if err := json.Unmarshal([]byte(c.kubectl(t, "get", "pod", pod, "--output=json")), &p); err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(p.Status.Ephemeral, func(s containerStatus) bool { return s.Name == name })
	if i < 0 {
		return stateJSON{}, false
	}
	var st stateJSON
	if err := json.Unmarshal(p.Status.Ephemeral[i].State, &st); err != nil {
		t.Fatal(err)
	}
	return st, true
}

// stateJSON is a container status's state.
type stateJSON struct {
	Running    *struct{} `json:"running"`
	Terminated *struct {
		Reason   string `json:"reason"`
		ExitCode int    `json:"exitCode"`
	} `json:"terminated"`
}

// killAppInit SIGKILLs the app container's init (sleep), as a crash or
// an OOM kill would end it: a process inside the container's PID
// namespace cannot kill its init. On kind it kills the pid `crictl
// inspect` names from the node container; on an existing cluster, where
// there is no node container but the operator's crictl prefix
// (IORMESH_K8S_CRICTL, task si4), through that prefix alone
// (crictlKill). The kubelet restarts the container. Without either the
// test skips, which fails mage k8sDebugRestart (gatecmd.KindTestsVerdict).
func (c *cluster) killAppInit(t *testing.T, pod string) {
	t.Helper()
	if len(c.cfg.crictl) == 0 {
		t.Skipf("%s and %s unset: no node to kill the app container's init from", envNode, envCrictl)
	}
	p := c.pod(t, "server")
	i := slices.IndexFunc(p.Status.Ctrs, func(s containerStatus) bool { return s.Name == "app" })
	if i < 0 {
		t.Fatalf("pod %s: no app container status", pod)
	}
	id := strings.TrimPrefix(p.Status.Ctrs[i].ContainerID, "containerd://")
	if c.cfg.node == "" {
		c.crictlKill(t, id)
		return
	}
	argv := append(slices.Clone(c.cfg.crictl), "inspect", "-o", "json", id)
	out, err := exec.Command(argv[0], argv[1:]...).Output()
	var v struct {
		Info struct {
			Pid int `json:"pid"`
		} `json:"info"`
	}
	if err == nil {
		err = json.Unmarshal(out, &v)
	}
	if err != nil || v.Info.Pid <= 1 {
		t.Fatalf("crictl inspect %s: pid %d, %v", id, v.Info.Pid, err)
	}
	if out, err := exec.Command("sudo", "-n", "docker", "exec", c.cfg.node, "kill", "-KILL",
		fmt.Sprint(v.Info.Pid)).CombinedOutput(); err != nil {
		t.Fatalf("kill %d on the node: %v\n%s", v.Info.Pid, err, out)
	}
	t.Logf("SIGKILLed the app container's init (node pid %d)", v.Info.Pid)
}

// crictlKill SIGKILLs container id's init through the crictl prefix
// alone: `crictl stop --timeout 0` skips the stop signal and the grace
// period, so containerd sends SIGKILL at once, as killAppInit's kill on
// kind's node does. The prefix (`ssh root@node k3s crictl`) runs crictl
// only, so a pid from `crictl inspect` could not be killed through it.
func (c *cluster) crictlKill(t *testing.T, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	argv := append(slices.Clone(c.cfg.crictl), "stop", "--timeout", "0", id)
	if out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("crictl stop --timeout 0 %s: %v\n%s", id, err, out)
	}
	t.Logf("SIGKILLed the app container's init (crictl stop --timeout 0 %s)", id)
}

// waitTracerEnded waits until pod's ephemeral tracer name terminated
// and the app container was restarted, and logs how the tracer ended.
func (c *cluster) waitTracerEnded(t *testing.T, pod, name string) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		st, ok := c.ephemeralState(t, pod, name)
		if ok && st.Terminated != nil {
			t.Logf("tracer %s ended: %s, exit code %d", name, st.Terminated.Reason, st.Terminated.ExitCode)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("tracer %s still not terminated a minute after the app container's init died", name)
		}
		time.Sleep(500 * time.Millisecond)
	}
	restarts := c.kubectl(t, "get", "pod", pod,
		`--output=jsonpath={.status.containerStatuses[?(@.name=="app")].restartCount}`)
	t.Logf("app container restart count: %s", restarts)
	c.kubectl(t, "wait", "pod/"+pod, "--for=condition=Ready", "--timeout=120s")
}

// waitContainerLog is waitLog for the tracer container name.
func (c *cluster) waitContainerLog(t *testing.T, pod, name, want string, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for {
		out, _ := c.try("logs", pod, "--container="+name)
		if strings.Contains(out, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pod %s: %s log lacks %q after %s:\n%s", pod, name, want, limit, out)
		}
		time.Sleep(time.Second)
	}
}

// segmentSession is a segment name's session id: <fqdn>-<session>-<start>-<seq>.
var segmentSession = regexp.MustCompile(`-([0-9a-f]{8})-[0-9]{8}T[0-9]{6}Z-[0-9]+\.`)

// storeSessions counts the collected segment files of dir by session.
func (c *cluster) storeSessions(t *testing.T, dir string) map[string]int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, e := range entries {
		if m := segmentSession.FindStringSubmatch(e.Name()); m != nil {
			got[m[1]]++
		}
	}
	return got
}

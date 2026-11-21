package kind

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// workloadJar is where the workload image keeps the jar.
const workloadJar = "/opt/workload/workload.jar"

// role is a workload process running in a pod through `kubectl exec`.
type role struct {
	name   string
	cmd    *exec.Cmd
	cancel context.CancelFunc
	mu     sync.Mutex
	out    bytes.Buffer
	done   chan error
}

// lockedWriter appends to the role's buffer under its lock.
type lockedWriter struct{ r *role }

func (w lockedWriter) Write(p []byte) (int, error) {
	w.r.mu.Lock()
	defer w.r.mu.Unlock()
	return w.r.out.Write(p)
}

// start runs argv in pod's app container, env set for the JVM (`env`
// is in every JRE image), and returns at once.
func (c *cluster) start(t *testing.T, name, pod string, env []string, argv ...string) *role {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	args := []string{"--context=" + c.cfg.context, "--namespace=" + c.ns, "exec", pod,
		"--container=app", "--", "env"}
	args = append(append(args, env...), argv...)
	r := &role{name: name, cancel: cancel, done: make(chan error, 1)}
	r.cmd = exec.CommandContext(ctx, "kubectl", args...)
	r.cmd.Env = append(os.Environ(), "KUBECONFIG="+c.cfg.kubeconfig)
	r.cmd.Stdout, r.cmd.Stderr = lockedWriter{r}, lockedWriter{r}
	if err := r.cmd.Start(); err != nil {
		cancel()
		t.Fatalf("%s: %v", name, err)
	}
	go func() { r.done <- r.cmd.Wait() }()
	t.Cleanup(cancel)
	return r
}

// output is what the role printed so far.
func (r *role) output() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.out.String()
}

// waitFor waits, checking every 100 ms up to limit, until the role printed
// want; a role that exited first fails the test.
func (r *role) waitFor(t *testing.T, want string, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !strings.Contains(r.output(), want) {
		select {
		case err := <-r.done:
			r.done <- err
			t.Fatalf("%s exited (%v) before printing %q:\n%s", r.name, err, want, r.output())
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: no %q after %s:\n%s", r.name, want, limit, r.output())
		}
	}
}

// wait waits for the role to exit 0 with its bracket closed.
func (r *role) wait(t *testing.T, scenario string) {
	t.Helper()
	if err := <-r.done; err != nil {
		t.Fatalf("%s: %v\n%s", r.name, err, r.output())
	}
	if !strings.Contains(r.output(), "marker "+scenario+"/end") {
		t.Fatalf("%s: no end marker:\n%s", r.name, r.output())
	}
}

// tcpPair runs tcp-nat's server in serverPod, bound to its pod address,
// and its client in clientPod dialing host:880 (a Service's ClusterIP,
// which kube-proxy DNATs to the server's :80), with the work files in
// /tmp/w of each.
func (c *cluster) tcpPair(t *testing.T, serverPod, serverIP, clientPod, host string) {
	t.Helper()
	for _, p := range []string{serverPod, clientPod} {
		c.kubectl(t, "exec", p, "--container=app", "--", "sh", "-c", "rm -rf /tmp/w && mkdir -p /tmp/w")
	}
	srv := c.start(t, "server", serverPod, nil, "java", "-jar", workloadJar, "tcp-nat",
		"--role", "server", "--bind", serverIP, "--port", "80", "--dir", "/tmp/w")
	srv.waitFor(t, "listening 80", time.Minute)
	cli := c.start(t, "client", clientPod, nil, "java", "-jar", workloadJar, "tcp-nat",
		"--role", "client", "--host", host, "--port", "880", "--dir", "/tmp/w")
	cli.wait(t, "tcp-nat")
	srv.wait(t, "tcp-nat")
}

//go:build mage

// M7's acceptance on a local kind cluster (task yb4, docs/testing.md "M7
// acceptance on kind"): K8sAcceptance stands up what the tests in
// integrationtests/kind need — a local registry, one kind cluster wired to
// it, the tracer and workload images pushed there (so a pod's imageID
// carries a registry digest), Traefik — runs them, and tears down exactly
// what it made (internal/kindrun). Every docker and kind call goes
// through `sudo -n`; nobody joins the docker group. Starting and stopping
// the Docker daemon is the operator's: without a usable `sudo -n docker`,
// kind, kubectl or helm the target says SKIP and succeeds.
//
// With IORMESH_K8S_KUBECONFIG set the run targets that existing cluster
// instead (task zg4, docs/testing.md "M7 on an existing cluster"): no
// kind cluster is made, kube-proxy's mode is the cluster's own, the run
// refuses namespaces (Traefik's among them) it did not create, and the
// teardown takes out only what the state file records the run put in,
// in the recorded context (kindrun.ClaimExisting, task li4).
//
// Never `docker system prune` here or in any teardown: the daemon holds
// the operator's own images and volumes (review N7).

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"

	"iormesh/internal/gatecmd"
	"iormesh/internal/kindrun"
)

// kindConfig points the node's containerd at /etc/containerd/certs.d,
// where the local registry is mapped (kind's local-registry setup).
const kindConfig = `kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
containerdConfigPatches:
- |-
  [plugins."io.containerd.grpc.v1.cri".registry]
    config_path = "/etc/containerd/certs.d"
`

// K8sAcceptance runs PLAN.md M7's acceptance on kind: TestKindAcceptance
// under kube-proxy's iptables mode, then TestKindClusterIP again under
// nftables, then K8sTeardown's steps. IORMESH_KIND_KEEP=1 keeps the
// cluster and the registry; IORMESH_KIND_KUBECONFIG puts the kubeconfig
// elsewhere. IORMESH_K8S_KUBECONFIG (with IORMESH_K8S_CONTEXT, else its
// current context, and IORMESH_K8S_CRICTL, the argv prefix that runs
// crictl on the node) targets an existing cluster instead.
// IORMESH_K8S_SELINUX_TYPE and IORMESH_K8S_SELINUX_LEVEL run the tracer at
// another SELinux type and level than spc_t at s0 (task 7h4, the opt-in
// iormesh_container_t; a level off s0 is pinned on every workload pod
// too). Ctrl-C (SIGINT) or SIGTERM starts the teardown in a process
// of its own session, which outlives mage's 5-second cancel; should even
// that fail, `mage k8sTeardown` is the recovery.
func K8sAcceptance() error {
	return kindRun("k8sAcceptance", kindTests)
}

// K8sDebugRestart runs task ah4's real-cluster check alone on kind
// (TestKindDebugRestart, docs/testing.md "App container restart"): a
// debug tracer with the agent dies with its app container's init, deploy
// starts iormesh-2, and collect gets both sessions' segments. Same
// setup, environment and teardown as K8sAcceptance, which it shares;
// kube-proxy's mode does not matter here and is left as it is. On an
// existing cluster the test kills the init through IORMESH_K8S_CRICTL;
// without it the test skips and the target fails (kindGoTest).
func K8sDebugRestart() error {
	return kindRun("k8sDebugRestart", func(env map[string]string, _ bool) error {
		return kindGoTest(env, "20m", "TestKindDebugRestart")
	})
}

// kindGoTest runs one test of ./integrationtests/kind with -v and judges
// it by what it reported, not by go test's exit status alone: a skipped
// test or subtest, or a test that never reported PASS, exits 0 too, and
// fails the target here (gatecmd.KindTestsVerdict, task si4), as `mage
// integrationTest` does.
func kindGoTest(env map[string]string, timeout, test string) error {
	var output bytes.Buffer
	w := io.MultiWriter(os.Stdout, &output)
	if _, err := sh.Exec(env, w, w, "go", "test", "./integrationtests/kind/", "-count=1", "-v",
		"-timeout="+timeout, "-run=^"+test+"$"); err != nil {
		return err
	}
	return gatecmd.KindTestsVerdict(output.String(), test)
}

// kindRun is the body K8sAcceptance and K8sDebugRestart share: skip
// without the prerequisites, build, disk guard, the teardown deferred
// (unless IORMESH_KIND_KEEP) and on signals, the cluster up, then tests.
// A run refused because the state file records another cluster's
// objects (kindrun.ErrForeignRecord) tears down with LeaveCluster: those
// objects and their record are left for an explicit `mage k8sTeardown`.
func kindRun(name string, tests func(env map[string]string, existing bool) error) error {
	if why := kindMissing(); why != "" {
		fmt.Println("SKIP "+name+":", why)
		return nil
	}
	c, err := kindConfigFor()
	if err != nil {
		return err
	}
	mg.Deps(Build, ReleaseTracer, AgentBuild, WorkloadBuild)
	r := shRunner{}
	if err := diskGuard(r); err != nil {
		return err
	}
	if os.Getenv("IORMESH_KIND_KEEP") == "" {
		var once sync.Once
		stop := teardownOnSignal(&once)
		defer stop()
		// The closure reads c when it runs, so LeaveCluster set below
		// reaches it.
		defer once.Do(func() { reportTeardown(kindrun.Teardown(r, c, os.Stdout)) })
	}
	env, err := kindUp(r, c)
	if errors.Is(err, kindrun.ErrForeignRecord) {
		c.LeaveCluster = true
	}
	if err != nil {
		return err
	}
	return tests(env, c.Existing)
}

// K8sTeardown removes what K8sAcceptance made — the cluster, the
// registry and its volume, the run's labelled images, the base images
// the run pulled (by the state recorded before it), the kubeconfig — and
// nothing else. In an existing cluster it removes only the release and
// namespaces the state file records, in the recorded kubeconfig and
// context, and nothing in the cluster without that record. Safe to
// repeat; the recovery after an interrupted run.
func K8sTeardown() error {
	if err := exec.Command("sudo", "-n", "docker", "info").Run(); err != nil {
		return fmt.Errorf("k8sTeardown needs the Docker daemon running (`sudo -n docker info` failed): %w", err)
	}
	c, err := kindConfigFor()
	if err != nil {
		return err
	}
	return kindrun.Teardown(shRunner{}, c, os.Stdout)
}

// kindConfigFor is the run's configuration: kubeconfig at the repository
// root unless $IORMESH_KIND_KUBECONFIG says otherwise, the base-image
// state beside it — or, with $IORMESH_K8S_KUBECONFIG, existingConfig.
func kindConfigFor() (kindrun.Config, error) {
	if os.Getenv("IORMESH_K8S_KUBECONFIG") != "" {
		return existingConfig()
	}
	kc := os.Getenv("IORMESH_KIND_KUBECONFIG")
	if kc == "" {
		kc = ".kind-kubeconfig"
	}
	abs, err := filepath.Abs(kc)
	if err == nil {
		kc = abs
	}
	return kindrun.Default(kc, kc+".state.json"), nil
}

// existingConfig is the configuration against the existing cluster of
// $IORMESH_K8S_KUBECONFIG, in $IORMESH_K8S_CONTEXT or else the
// kubeconfig's current context; the base-image state goes to the
// repository root (the kubeconfig's directory is the operator's). The
// context is looked up anew on every invocation, so the teardown does
// not use it for the cluster: it uses the context the state file
// recorded when the run claimed its namespaces.
func existingConfig() (kindrun.Config, error) {
	kc, err := filepath.Abs(os.Getenv("IORMESH_K8S_KUBECONFIG"))
	if err != nil {
		return kindrun.Config{}, err
	}
	ctx := os.Getenv("IORMESH_K8S_CONTEXT")
	if ctx == "" {
		out, err := sh.Output("kubectl", "--kubeconfig="+kc, "config", "current-context")
		if err != nil {
			return kindrun.Config{}, fmt.Errorf("IORMESH_K8S_KUBECONFIG %s: no current context: %w", kc, err)
		}
		ctx = strings.TrimSpace(out)
	}
	state, err := filepath.Abs(".k8s-existing.state.json")
	return kindrun.Existing(kc, ctx, state), err
}

// teardownOnSignal starts the teardown, once, in a new session when
// SIGINT or SIGTERM arrives: mage gives cancelled targets 5 seconds and
// a terminal's Ctrl-C reaches the whole process group, but not a
// setsid'd child. The returned func stops listening.
func teardownOnSignal(once *sync.Once) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig, ok := <-ch
		if !ok {
			return
		}
		once.Do(func() { detachedTeardown(sig) })
		os.Exit(1)
	}()
	return func() { signal.Stop(ch); close(ch) }
}

// detachedTeardown runs this mage binary's k8sTeardown in its own
// session, its output in k8s-teardown.log, and does not wait for it.
func detachedTeardown(sig os.Signal) {
	self, err := os.Executable()
	log, lerr := os.Create("k8s-teardown.log")
	if err != nil || lerr != nil {
		fmt.Printf("%v: cannot start the teardown (%v %v): run `mage k8sTeardown`\n", sig, err, lerr)
		return
	}
	cmd := exec.Command(self, "k8sTeardown")
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fmt.Printf("%v: cannot start the teardown (%v): run `mage k8sTeardown`\n", sig, err)
		return
	}
	fmt.Printf("%v: teardown running as pid %d, log in k8s-teardown.log\n", sig, cmd.Process.Pid)
}

// reportTeardown prints a teardown's failure: the run's result stands.
func reportTeardown(err error) {
	if err != nil {
		fmt.Printf("teardown: %v (run `mage k8sTeardown` again once fixed)\n", err)
	}
}

// kindMissing names the first missing prerequisite, "" when all are
// there; an existing cluster needs no kind.
func kindMissing() string {
	bins := []string{"kind", "kubectl", "helm", "docker"}
	if os.Getenv("IORMESH_K8S_KUBECONFIG") != "" {
		bins = bins[1:]
	}
	for _, bin := range bins {
		if _, err := exec.LookPath(bin); err != nil {
			return bin + " is not on PATH"
		}
	}
	if err := exec.Command("sudo", "-n", "docker", "info").Run(); err != nil {
		return "`sudo -n docker info` failed (is the daemon running, may sudo run docker?)"
	}
	return ""
}

// kindUp records the base images, claims an existing cluster's
// namespaces (refusing ones it did not create, and recording what it
// will create there and in which context, before anything touches the
// cluster: task li4), brings up the registry, the cluster (unless it is
// an existing one), the images and Traefik, and returns the tests'
// environment.
func kindUp(r shRunner, c kindrun.Config) (map[string]string, error) {
	state, err := kindrun.Snapshot(r, c)
	if err == nil {
		err = c.Save(state)
	}
	if err == nil && c.Existing {
		err = kindrun.ClaimExisting(r, c)
	}
	if err == nil {
		err = kindrun.RegistryUp(r, c)
	}
	if err == nil && !c.Existing {
		err = kindClusterUp(r, c)
	}
	if err != nil {
		return nil, err
	}
	tracer, err := kindTracerImage(r, c)
	if err != nil {
		return nil, err
	}
	workload, err := kindWorkloadImage(r, c)
	if err != nil {
		return nil, err
	}
	if err := kindTraefik(c); err != nil {
		return nil, err
	}
	ctl, err := filepath.Abs(ctlBinaryName)
	return map[string]string{"IORMESH_KIND_KUBECONFIG": c.Kubeconfig, "IORMESH_KIND_CONTEXT": c.Context,
		"IORMESH_KIND_TRACER_IMAGE": tracer, "IORMESH_KIND_WORKLOAD_IMAGE": workload,
		"IORMESH_KIND_CTL": ctl, "IORMESH_KIND_NODE": c.Node,
		"IORMESH_KIND_CRICTL":        os.Getenv("IORMESH_K8S_CRICTL"),
		"IORMESH_KIND_SELINUX_TYPE":  os.Getenv("IORMESH_K8S_SELINUX_TYPE"),
		"IORMESH_KIND_SELINUX_LEVEL": os.Getenv("IORMESH_K8S_SELINUX_LEVEL"), "CGO_ENABLED": "0"}, err
}

// kindTests runs the suite under iptables, then the ClusterIP join under
// nftables. An existing cluster's kube-proxy is not kind's daemonset
// (k3s embeds it), so there both run in the cluster's own mode, and the
// operator reruns after switching it. Each run fails on a skip or a test
// not reported as passed (kindGoTest): on an existing cluster without
// IORMESH_K8S_CRICTL the SIGTERM check skips, and the target fails.
func kindTests(env map[string]string, existing bool) error {
	run := func(test string) error { return kindGoTest(env, "40m", test) }
	if existing {
		fmt.Println("NOTICE existing cluster: kube-proxy's mode is the cluster's own, not switched")
		return errors.Join(run("TestKindAcceptance"), run("TestKindClusterIP"))
	}
	if err := kindProxyMode(env, "iptables"); err != nil {
		return err
	}
	if err := run("TestKindAcceptance"); err != nil {
		return err
	}
	if err := kindProxyMode(env, "nftables"); err != nil {
		return err
	}
	return run("TestKindClusterIP")
}

// kubectlKind runs kubectl in the acceptance context.
func kubectlKind(env map[string]string, args ...string) (string, error) {
	return sh.OutputWith(map[string]string{"KUBECONFIG": env["IORMESH_KIND_KUBECONFIG"]}, "kubectl",
		append([]string{"--context=" + env["IORMESH_KIND_CONTEXT"]}, args...)...)
}

// proxyModeLine is the mode line of kube-proxy's config.conf as the
// configmap's YAML indents it.
var proxyModeLine = regexp.MustCompile(`(?m)^    mode: [a-z]*$`)

// kindProxyMode sets kube-proxy's mode and restarts it; a cluster already
// in that mode is left alone.
func kindProxyMode(env map[string]string, mode string) error {
	cm, err := kubectlKind(env, "--namespace=kube-system", "get", "configmap", "kube-proxy", "--output=yaml")
	if err != nil {
		return err
	}
	line := proxyModeLine.FindString(cm)
	if line == "" {
		return errors.New("kube-proxy's configmap names no mode")
	}
	if line == "    mode: "+mode {
		return nil
	}
	path, err := writeTemp("iormesh-kube-proxy-*.yaml", strings.Replace(cm, line, "    mode: "+mode, 1))
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(path) }()
	for _, args := range [][]string{{"apply", "--filename=" + path},
		{"--namespace=kube-system", "rollout", "restart", "daemonset/kube-proxy"},
		{"--namespace=kube-system", "rollout", "status", "daemonset/kube-proxy", "--timeout=120s"}} {
		if _, err := kubectlKind(env, args...); err != nil {
			return err
		}
	}
	return nil
}

// writeTemp writes text to a new private temporary file (os.CreateTemp:
// a fixed name in /tmp could be planted) and returns its path.
func writeTemp(pattern, text string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	_, werr := f.WriteString(text)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// kindClusterUp creates the cluster unless it exists, writes the
// kubeconfig as the operator, and wires the registry into the node.
func kindClusterUp(r shRunner, c kindrun.Config) error {
	out, err := r.Output("sudo", "-n", "kind", "get", "clusters")
	if err != nil {
		return err
	}
	if !strings.Contains("\n"+out+"\n", "\n"+c.Cluster+"\n") {
		cfg, err := writeTemp("iormesh-kind-*.yaml", kindConfig)
		if err != nil {
			return err
		}
		defer func() { _ = os.Remove(cfg) }()
		if err := kindrun.CreateCluster(r, c, cfg); err != nil {
			return err
		}
	}
	if err := kindrun.WriteKubeconfig(r, c); err != nil {
		return err
	}
	// Already connected is no error worth stopping for.
	_ = exec.Command("sudo", "-n", "docker", "network", "connect", "kind", c.Registry).Run()
	dir := "/etc/containerd/certs.d/" + kindrun.RegistryAt
	hosts := fmt.Sprintf("[host.\"http://%s:5000\"]\n", c.Registry)
	return r.Run("sudo", "-n", "docker", "exec", c.Node, "sh", "-c",
		"mkdir -p "+dir+" && printf '%s' '"+hosts+"' > "+dir+"/hosts.toml")
}

// kindTraefik installs Traefik (CRDs under traefik.io included) with a
// ClusterIP Service, from a helm repository config of its own.
func kindTraefik(c kindrun.Config) error {
	dir, err := os.MkdirTemp("", "iormesh-helm-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	for _, argv := range kindrun.HelmArgv(c, dir, c.Kubeconfig) {
		if err := sh.RunV(argv[0], argv[1:]...); err != nil {
			return err
		}
	}
	return nil
}

// shRunner runs argv through mage's sh helpers.
type shRunner struct{}

func (shRunner) Run(argv ...string) error { return sh.RunV(argv[0], argv[1:]...) }

func (shRunner) Output(argv ...string) (string, error) { return sh.Output(argv[0], argv[1:]...) }

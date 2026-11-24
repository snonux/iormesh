package ctl

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/k8s"
)

// podOnlyRegistry is podRegistry's pod: entry alone, so a bare `ensure`
// reaches it and nothing else.
const podOnlyRegistry = `servers: []
k8s:
  image: ` + podImage + `
  clusters:
    - name: kind
      context: kind-test
      namespace: shop
pods:
  - cluster: kind
    pod: checkout-0
    container: app
    projects:
      - target: {pattern: "*java*"}
        flags: ["-duration", "1h"]
`

// stoppedTracer is a pod whose one debug tracer terminated with code.
func stoppedTracer(reason string, code int) map[string]any {
	obj := tracerObject("checkout-0", uid(1), map[string]bool{"iormesh": false})
	obj["status"].(map[string]any)["ephemeralContainerStatuses"] = []map[string]any{{"name": k8s.TracerContainer,
		"imageID": podImage, "state": map[string]any{"terminated": map[string]any{"reason": reason, "exitCode": code}}}}
	return obj
}

// TestEnsurePodRestartsEnded (task pk4): a bare ensure reaches the pod
// entries too. A debug tracer killed with its app container (exit 137)
// ended on its own: ensure starts iormesh-2 through deploy's pod path —
// the deploy argv, the attach wait — and then collects through the new
// tracer.
func TestEnsurePodRestartsEnded(t *testing.T) {
	f := newPodFixture(t, podOnlyRegistry)
	f.pod(t, stoppedTracer("Error", 137))
	after, err := json.Marshal(tracerObject("checkout-0", uid(1), map[string]bool{"iormesh": false, "iormesh-2": true}))
	if err != nil {
		t.Fatal(err)
	}
	f.file(t, "after-checkout-0.json", after)
	if code := f.run("ensure"); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr)
	}
	calls := f.calls(t)
	got := verbs(calls)
	want := []string{"get pod", "get pod", "debug checkout-0", "exec checkout-0", "get pod", "exec checkout-0"}
	if !slices.Equal(got, want) {
		t.Fatalf("calls %q, want %q", got, want)
	}
	dbg := calls[2]
	i := slices.Index(dbg, "--")
	wantArgv := []string{"iormesh", "run", "-project", "shop", "-linger", "-pattern=*java*", "-duration", "1h",
		"-disk-limit", "0", "-disk-max-bytes", "1536MiB"}
	if i < 0 || !slices.Equal(dbg[i+1:], wantArgv) || !slices.Contains(dbg, "--container=iormesh-2") {
		t.Errorf("debug argv %q", dbg)
	}
	if fetch := calls[5]; !slices.Contains(fetch, "--container=iormesh-2") || !slices.Contains(fetch, "ready") {
		t.Errorf("collect %q does not go through iormesh-2", fetch)
	}
	out := f.stdout.String()
	for _, w := range []string{"started tracer iormesh-2, project shop",
		`kind/shop/checkout-0 pod checkout-0: project shop: tracer container "iormesh" terminated ` +
			"(Error, exit code 137), restarted"} {
		if !strings.Contains(out, w) {
			t.Errorf("stdout lacks %q:\n%s", w, out)
		}
	}
}

// TestEnsurePodLeavesStopped: a debug tracer that exited 0 was ended on
// purpose (a SIGTERM; -linger never exits on its own), so ensure starts
// nothing and, since kubectl exec cannot enter it, skips its collect
// with a note rather than failing every timer tick on it.
func TestEnsurePodLeavesStopped(t *testing.T) {
	f := newPodFixture(t, podOnlyRegistry)
	f.pod(t, stoppedTracer("Completed", 0))
	if code := f.run("ensure", "kind/shop/checkout-0"); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr)
	}
	if got := verbs(f.calls(t)); slices.ContainsFunc(got, func(v string) bool { return !strings.HasPrefix(v, "get") }) {
		t.Errorf("calls %q, want pod reads alone", got)
	}
	out := f.stdout.String()
	for _, w := range []string{"exit code 0), stopped on purpose, left stopped", "collect skipped"} {
		if !strings.Contains(out, w) {
			t.Errorf("stdout lacks %q:\n%s", w, out)
		}
	}
}

// TestEnsurePodSelectorOneFails (review round 1): in a selector entry,
// a pod whose restart fails — its eighth debug tracer killed, the most
// deploy starts — is reported ("restart failed", exit 1), and the next
// pod is still ensured and collected.
func TestEnsurePodSelectorOneFails(t *testing.T) {
	f := newPodFixture(t, "")
	maxed := map[string]bool{}
	for n := 1; n <= k8s.MaxDebugTracers; n++ {
		maxed[k8s.TracerName(n)] = false
	}
	webA := tracerObject("web-a", uid(1), maxed)
	f.pod(t, webA)
	f.list(t, webA, podObject("web-b", uid(2), true))
	if code := f.run("ensure", "kind/shop/app=web"); code != exitError {
		t.Fatalf("exit %d, want %d: %s", code, exitError, f.stderr)
	}
	calls := f.calls(t)
	if got := verbs(calls); slices.Contains(got, "debug web-a") {
		t.Errorf("started a ninth tracer: %q", got)
	}
	if !slices.ContainsFunc(calls, func(c []string) bool {
		return c[0] == "exec" && c[1] == "web-b" && slices.Contains(c, "ready") && !slices.Contains(c, "-state") &&
			!slices.Contains(c, "-status")
	}) {
		t.Errorf("web-b not collected after web-a failed: %q", calls)
	}
	out := f.stdout.String()
	for _, w := range []string{"kind/shop/app=web pod web-a: project web: tracer container \"iormesh-8\" " +
		"terminated (Error, exit code 137), restart failed", "kind/shop/app=web pod web-b: project web: running"} {
		if !strings.Contains(out, w) {
			t.Errorf("stdout lacks %q:\n%s", w, out)
		}
	}
	for _, w := range []string{"pod web-a:", "the most deploy starts", "ensure failed on 1 of 1 targets"} {
		if !strings.Contains(f.stderr.String(), w) {
			t.Errorf("stderr lacks %q: %s", w, f.stderr)
		}
	}
}

// TestEnsurePodTerminating (review round 1): a pod: entry's pod in its
// deletion grace period still reads phase Running, and its tracer dies
// with the PID namespace (exit 137). ensure must not `kubectl debug`
// into it — the tracer would never attach — but say why it left it.
func TestEnsurePodTerminating(t *testing.T) {
	f := newPodFixture(t, podOnlyRegistry)
	obj := stoppedTracer("Error", 137)
	obj["metadata"].(map[string]any)["deletionTimestamp"] = "2026-11-24T08:00:00Z"
	f.pod(t, obj)
	f.run("ensure")
	if got := verbs(f.calls(t)); slices.Contains(got, "debug checkout-0") {
		t.Errorf("started a tracer in a terminating pod: %q", got)
	}
	if w := "pod terminating, nothing restarted"; !strings.Contains(f.stdout.String(), w) {
		t.Errorf("stdout lacks %q:\n%s", w, f.stdout)
	}
}

// TestEnsurePodVanished: a pod: entry whose pod is gone fails the entry
// (exit 1, kubectl's NotFound), as collect does; nothing is started.
func TestEnsurePodVanished(t *testing.T) {
	f := newPodFixture(t, podOnlyRegistry)
	if code := f.run("ensure"); code != exitError {
		t.Fatalf("exit %d, want %d: %s", code, exitError, f.stderr)
	}
	if got := verbs(f.calls(t)); !slices.Equal(got, []string{"get pod"}) {
		t.Errorf("calls %q, want the get alone", got)
	}
	for _, w := range []string{"kind/shop/checkout-0", "not found", "nothing restarted or collected",
		"ensure failed on 1 of 1 targets"} {
		if !strings.Contains(f.stderr.String(), w) {
			t.Errorf("stderr lacks %q: %s", w, f.stderr)
		}
	}
}

// TestEnsurePodDecisions: a running tracer whose session runs is left
// and collected; one whose session ended lingers in a container no
// deploy can restart, so ensure says a pod restart is needed and starts
// nothing; a pod without a tracer container (a restarted pod) is
// deploy's. None of them fails.
func TestEnsurePodDecisions(t *testing.T) {
	for _, c := range []struct {
		name   string
		tracer bool
		state  string
		note   string
	}{
		{"running", true, "running 1a2b3c4d", "project shop: running, nothing to do"},
		{"ended lingering", true, "ended duration", "project shop: ended (duration): " +
			"a running debug tracer cannot be restarted, restart the pod for a new session"},
		{"no tracer", false, "", "project shop: no tracer container: deploy starts it, ensure only restarts"},
	} {
		f := newPodFixture(t, podOnlyRegistry)
		f.pod(t, podObject("checkout-0", uid(1), c.tracer))
		if c.state != "" {
			f.file(t, "state-checkout-0", []byte(c.state+"\n"))
		}
		if code := f.run("ensure"); code != exitOK {
			t.Errorf("%s: exit %d: %s", c.name, code, f.stderr)
			continue
		}
		if got := verbs(f.calls(t)); slices.Contains(got, "debug checkout-0") {
			t.Errorf("%s: started a tracer: %q", c.name, got)
		}
		if !strings.Contains(f.stdout.String(), c.note) {
			t.Errorf("%s: stdout lacks %q:\n%s", c.name, c.note, f.stdout)
		}
	}
}

// TestEnsurePodRefusesFlags: a debug entry whose flags deploy would
// refuse fails before any pod is contacted, since a restart runs
// deploy's argv.
func TestEnsurePodRefusesFlags(t *testing.T) {
	f := newPodFixture(t, strings.Replace(podOnlyRegistry, `["-duration", "1h"]`, `["-disk-limit", "5"]`, 1))
	f.pod(t, stoppedTracer("Error", 137))
	if code := f.run("ensure"); code == exitOK || len(f.calls(t)) != 0 {
		t.Errorf("exit %d, calls %q", code, f.calls(t))
	}
	if !strings.Contains(f.stderr.String(), "deploy sets it from the pod") {
		t.Errorf("stderr: %s", f.stderr)
	}
}

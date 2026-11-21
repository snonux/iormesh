package ctl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/federate"
	"iormesh/internal/k8s"
)

// fakeKubectl stands in for kubectl on PATH (as internal/k8s's tests do):
// it logs its argv (NUL-terminated elements, an RS element per call) and
// answers from files in $FAKE_DIR — pod-NAME.json for `get pod NAME`
// (absent: kubectl's NotFound, exit 1), list.json for `get pods
// --selector`, tar-NAME for `exec NAME … ready` (else an empty stream),
// and down-NAME makes every exec into NAME fail as kubectl itself does
// (exit 1, no "command terminated" line). No cluster is reached.
const fakeKubectl = `#!/bin/sh
for a in "$@"; do printf '%s\0' "$a" >> "$FAKE_DIR/log"; done
printf '\036\0' >> "$FAKE_DIR/log"
shift 2
verb="$1"; shift
case "$verb" in
get)
  if [ "$1" = pods ]; then cat "$FAKE_DIR/list.json"; exit 0; fi
  if [ ! -f "$FAKE_DIR/pod-$2.json" ]; then
    echo "Error from server (NotFound): pods \"$2\" not found" >&2; exit 1
  fi
  cat "$FAKE_DIR/pod-$2.json" ;;
exec)
  pod="$1"
  if [ -f "$FAKE_DIR/down-$pod" ]; then
    echo "error: unable to upgrade connection: container not found" >&2; exit 1
  fi
  case " $* " in *" -remove "*) exit 0 ;; esac
  if [ -f "$FAKE_DIR/tar-$pod" ]; then cat "$FAKE_DIR/tar-$pod"; else cat "$FAKE_DIR/empty.tar"; fi ;;
debug) echo "ephemeral container iormesh created" ;;
esac
`

// podImage is a digest-pinned tracer image (placeholder, #18).
const podImage = "registry.example/iormesh@sha256:" +
	"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// podRegistry is a registry with one named pod and one selector entry,
// both debug mode in cluster kind's namespace shop (placeholders, #18).
const podRegistry = `servers: []
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
  - cluster: kind
    selector: app=web
    container: app
    projects:
      - name: web
        target: {listen_port: 8080}
`

// podFixture is one test's iormeshctl env with the fake kubectl.
type podFixture struct {
	e              env
	dir, registry  string
	store, share   string
	stdout, stderr *bytes.Buffer
}

// newPodFixture writes podRegistry (with registry's text when given)
// and puts the fake kubectl first on PATH.
func newPodFixture(t *testing.T, registry string) *podFixture {
	t.Helper()
	if registry == "" {
		registry = podRegistry
	}
	f := &podFixture{dir: t.TempDir(), store: filepath.Join(t.TempDir(), "traces"),
		share: t.TempDir(), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	cfg := filepath.Join(t.TempDir(), "iormesh")
	if err := os.Mkdir(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	f.registry = filepath.Join(cfg, "servers.yaml")
	writeFile(t, f.registry, []byte(registry), 0o600)
	bin := filepath.Join(f.dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(bin, "kubectl"), []byte(fakeKubectl), 0o700)
	writeFile(t, filepath.Join(f.dir, "empty.tar"), segmentTar(t, nil), 0o600)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DIR", f.dir)
	f.e = env{stdout: f.stdout, stderr: f.stderr,
		registry:  func() (string, error) { return f.registry, nil },
		traces:    func() (string, error) { return f.store, nil },
		servicedb: func() (string, error) { return filepath.Join(f.share, "services.db"), nil }}
	return f
}

// run runs iormeshctl with a fresh log and output.
func (f *podFixture) run(args ...string) int {
	_ = os.Remove(filepath.Join(f.dir, "log"))
	f.stdout.Reset()
	f.stderr.Reset()
	return run(args, f.e)
}

// calls parses the fake's log into one argv per kubectl call, the
// context and namespace dropped.
func (f *podFixture) calls(t *testing.T) [][]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, "log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	var cur []string
	for _, a := range bytes.Split(bytes.TrimSuffix(b, []byte{0}), []byte{0}) {
		if string(a) == "\x1e" {
			if len(cur) < 2 || cur[0] != "--context=kind-test" || cur[1] != "--namespace=shop" {
				t.Errorf("call without the registry's context and namespace: %q", cur)
			}
			calls = append(calls, cur[2:])
			cur = nil
			continue
		}
		cur = append(cur, string(a))
	}
	return calls
}

// file writes name in the fake's directory.
func (f *podFixture) file(t *testing.T, name string, data []byte) {
	t.Helper()
	writeFile(t, filepath.Join(f.dir, name), data, 0o600)
}

// uid is a pod UID ending in n.
func uid(n int) string { return fmt.Sprintf("0123abcd-0000-4000-8000-%012d", n) }

// podObject is a pod as `kubectl get` prints it: prepared (#13's
// volumes), running, an app container, and when tracer is set an
// ephemeral tracer of the pinned image, running.
func podObject(name, id string, tracer bool) map[string]any {
	spec := map[string]any{"containers": []map[string]any{{"name": "app"}},
		"volumes": []map[string]any{
			{"name": k8s.TracefsVolume, "hostPath": map[string]any{"path": k8s.TracefsPath}},
			{"name": k8s.TracesVolume, "emptyDir": map[string]any{"sizeLimit": "2Gi"}}}}
	status := map[string]any{"phase": "Running"}
	if tracer {
		spec["ephemeralContainers"] = []map[string]any{{"name": k8s.TracerContainer, "image": podImage}}
		status["ephemeralContainerStatuses"] = []map[string]any{{"name": k8s.TracerContainer,
			"imageID": podImage, "state": map[string]any{"running": map[string]any{}}}}
	}
	return map[string]any{"metadata": map[string]any{"name": name, "uid": id}, "spec": spec, "status": status}
}

// pod serves obj for `get pod NAME`.
func (f *podFixture) pod(t *testing.T, obj map[string]any) {
	t.Helper()
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	f.file(t, "pod-"+obj["metadata"].(map[string]any)["name"].(string)+".json", b)
}

// list serves objs for `get pods --selector`.
func (f *podFixture) list(t *testing.T, objs ...map[string]any) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"items": objs})
	if err != nil {
		t.Fatal(err)
	}
	f.file(t, "list.json", b)
}

// segmentTar is the stream `iormesh ready` writes for a ready/ holding
// the named segments (k8s.WriteReady itself, so the format is the
// tracer's).
func segmentTar(t *testing.T, names []string) []byte {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		writeFile(t, filepath.Join(dir, n), []byte("seg "+n), 0o600)
	}
	var buf bytes.Buffer
	if err := k8s.WriteReady(&buf, io.Discard, dir); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// verbs is each call's verb and first operand.
func verbs(calls [][]string) []string {
	var out []string
	for _, c := range calls {
		v := c[0] + " " + c[1]
		if c[0] == "exec" && slices.Contains(c, "-remove") {
			v += " -remove"
		}
		out = append(out, v)
	}
	return out
}

// TestPodCollect: collect of a named pod entry asks the API server for
// the pod, pins pod-<uid> to the pod's identity, fetches through `exec …
// iormesh ready` into <store>/<namespace project>/pod-<uid>, removes
// what arrived — and runs none of the SSH path's shell probes.
func TestPodCollect(t *testing.T) {
	f := newPodFixture(t, "")
	f.pod(t, podObject("checkout-0", uid(1), true))
	seg := "checkout-0.shop.0123abcd-s1-1-0.parquet"
	f.file(t, "tar-checkout-0", segmentTar(t, []string{seg}))
	if code := f.run("collect", "kind/shop/checkout-0"); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr)
	}
	calls := f.calls(t)
	want := []string{"get pod", "exec checkout-0", "exec checkout-0 -remove"}
	if got := verbs(calls); !slices.Equal(got, want) {
		t.Fatalf("calls %q, want %q", calls, want)
	}
	if !slices.Equal(calls[1][2:], []string{"--container=iormesh", "--", "iormesh", "ready", "-project", "shop"}) {
		t.Errorf("fetch argv %q", calls[1])
	}
	for _, c := range calls {
		if slices.Contains(c, "true") || slices.Contains(c, "test") || slices.Contains(c, "find") {
			t.Errorf("shell probe in a shell-less image: %q", c)
		}
	}
	dir := filepath.Join(f.store, "shop", k8s.PodHostID(uid(1)))
	if b, err := os.ReadFile(filepath.Join(dir, seg)); err != nil || string(b) != "seg "+seg {
		t.Errorf("segment: %q, %v", b, err)
	}
	if !strings.Contains(f.stdout.String(), "kind/shop/checkout-0 pod checkout-0: project shop: 1 new files") {
		t.Errorf("stdout: %s", f.stdout)
	}
	pins, err := loadHostPins(filepath.Join(f.share, hostPinsFileName))
	wantPin := federate.HostIdentity{HostID: "pod-" + uid(1), FQDN: "checkout-0.shop.0123abcd"}
	if err != nil || pins["pod-"+uid(1)] != wantPin {
		t.Errorf("pins %v, %v; want %v", pins, err, wantPin)
	}
}

// TestPodCollectUnreachable: a pod kubectl cannot exec into is a failed
// collect (exit 1, ErrUnreachable's text), never "nothing to collect" or
// "0 new files" — an unreached pod is not an empty one.
func TestPodCollectUnreachable(t *testing.T) {
	f := newPodFixture(t, "")
	f.pod(t, podObject("checkout-0", uid(1), true))
	f.file(t, "down-checkout-0", nil)
	if code := f.run("collect", "kind/shop/checkout-0"); code != exitError {
		t.Fatalf("exit %d, want %d", code, exitError)
	}
	if !strings.Contains(f.stderr.String(), "pod not reached") ||
		!strings.Contains(f.stderr.String(), "collect failed on 1 of 1 hosts: kind/shop/checkout-0") {
		t.Errorf("stderr: %s", f.stderr)
	}
	if out := f.stdout.String(); strings.Contains(out, "nothing to collect") || strings.Contains(out, "new files") {
		t.Errorf("unreached pod reported as collected: %s", out)
	}
	// A pod: entry's pod that is gone (kubectl NotFound) fails too.
	_ = os.Remove(filepath.Join(f.dir, "pod-checkout-0.json"))
	if code := f.run("collect", "kind/shop/checkout-0"); code != exitError ||
		strings.Contains(f.stdout.String(), "nothing to collect") {
		t.Errorf("gone pod: exit %d, stdout %s", code, f.stdout)
	}
}

// TestPodCollectStopped: a tracer container that terminated anyway (an
// OOM kill, a crash — deploy's -linger keeps a finished session's
// running) fails the collect saying the session ended and its segments
// are stranded, without trying an exec kubectl would refuse.
func TestPodCollectStopped(t *testing.T) {
	f := newPodFixture(t, "")
	obj := podObject("checkout-0", uid(1), true)
	obj["status"].(map[string]any)["ephemeralContainerStatuses"] = []map[string]any{{"name": k8s.TracerContainer,
		"state": map[string]any{"terminated": map[string]any{"reason": "OOMKilled", "exitCode": 137}}}}
	f.pod(t, obj)
	if code := f.run("collect", "kind/shop/checkout-0"); code != exitError {
		t.Fatalf("exit %d, want %d", code, exitError)
	}
	for _, want := range []string{"session ended", "terminated (OOMKilled, exit code 137)", "stranded"} {
		if !strings.Contains(f.stderr.String(), want) {
			t.Errorf("stderr lacks %q: %s", want, f.stderr)
		}
	}
	if got := verbs(f.calls(t)); !slices.Equal(got, []string{"get pod"}) {
		t.Errorf("calls %q, want the get alone", got)
	}
}

// TestPodCollectNoTracer: a running pod the API server shows without a
// tracer container has nothing to collect — said, not an error, and no
// exec is tried.
func TestPodCollectNoTracer(t *testing.T) {
	f := newPodFixture(t, "")
	f.pod(t, podObject("checkout-0", uid(1), false))
	if code := f.run("collect", "kind/shop/checkout-0"); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr)
	}
	if got := verbs(f.calls(t)); !slices.Equal(got, []string{"get pod"}) {
		t.Errorf("calls %q", got)
	}
	if !strings.Contains(f.stdout.String(), `no "iormesh" tracer container, nothing to collect`) {
		t.Errorf("stdout: %s", f.stdout)
	}
}

// TestPodCollectSelector: a selector entry collects every running pod it
// matches, each into its own pod-<uid>; one matching nothing fails the
// collect (PLAN.md #18), and `collect` without targets includes the pod
// entries.
func TestPodCollectSelector(t *testing.T) {
	f := newPodFixture(t, "")
	f.list(t, podObject("web-b", uid(3), true), podObject("web-a", uid(2), true))
	f.pod(t, podObject("checkout-0", uid(1), true))
	if code := f.run("collect"); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr)
	}
	want := []string{"get pod", "exec checkout-0", "get pods", "exec web-a", "exec web-b"}
	if got := verbs(f.calls(t)); !slices.Equal(got, want) {
		t.Errorf("calls %q, want %q", got, want)
	}
	for _, n := range []int{2, 3} {
		if fi, err := os.Stat(filepath.Join(f.store, "web", k8s.PodHostID(uid(n)))); err != nil || !fi.IsDir() {
			t.Errorf("pod %d's directory: %v", n, err)
		}
	}
	f.list(t)
	if code := f.run("collect", "kind/shop/app=web"); code != exitError ||
		!strings.Contains(f.stderr.String(), "no running pod") {
		t.Errorf("no match: exit %d, stderr %s", code, f.stderr)
	}
}

// TestPodRefusedBeforeContact: an unregistered -context, an unknown pod
// entry, and (for deploy) an unpinned -image are refused before any
// kubectl runs.
func TestPodRefusedBeforeContact(t *testing.T) {
	f := newPodFixture(t, "")
	for _, c := range []struct {
		args []string
		msg  string
	}{
		{[]string{"collect", "-context", "prod", "kind/shop/checkout-0"}, `-context "prod" is not cluster kind's`},
		{[]string{"collect", "-context", "-x", "kind/shop/checkout-0"}, "context refused"},
		{[]string{"collect", "kind/shop/other"}, "pod entry not registered"},
		{[]string{"deploy", "-context", "prod", "kind/shop/checkout-0"}, "context refused"},
		{[]string{"deploy", "-image", "registry.example/iormesh:latest", "kind/shop/checkout-0"}, "not pinned by digest"},
		{[]string{"collect", "-namespace", "other", "-project", "x"}, "not taken by a collect of every target"},
		{[]string{"refresh", "-context", "kind-test"}, "not taken by a collect of every target"},
		{[]string{"rm", "-context", "kind-test", "kind/shop/checkout-0"}, "not taken by rm"},
		{[]string{"rm", "-namespace", "other", "kind/shop/checkout-0"}, "not taken by rm"},
	} {
		if code := f.run(c.args...); code == exitOK || !strings.Contains(f.stderr.String(), c.msg) {
			t.Errorf("%v: exit %d, stderr %s", c.args, code, f.stderr)
		}
		if calls := f.calls(t); len(calls) != 0 {
			t.Errorf("%v ran kubectl: %q", c.args, calls)
		}
	}
}

// TestPodDeploy: a prepared pod without a tracer gets the debug container
// running the entry's project — `iormesh run`, the target, its flags;
// no uname, command -v, mkdir or push.
func TestPodDeploy(t *testing.T) {
	f := newPodFixture(t, "")
	f.pod(t, podObject("checkout-0", uid(1), false))
	if code := f.run("deploy", "kind/shop/checkout-0"); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr)
	}
	calls := f.calls(t)
	if got := verbs(calls); !slices.Equal(got, []string{"get pod", "get pod", "debug checkout-0"}) {
		t.Fatalf("calls %q", calls)
	}
	dbg := calls[2]
	i := slices.Index(dbg, "--")
	wantArgv := []string{"iormesh", "run", "-project", "shop", "-linger", "-pattern=*java*", "-duration", "1h",
		"-disk-limit", "0", "-disk-max-bytes", "1536MiB"}
	if i < 0 || !slices.Equal(dbg[i+1:], wantArgv) || !slices.Contains(dbg, "--image="+podImage) ||
		!slices.Contains(dbg, "--target=app") {
		t.Errorf("debug argv %q", dbg)
	}
	if !strings.Contains(f.stdout.String(), "started tracer, project shop") {
		t.Errorf("stdout: %s", f.stdout)
	}
}

// TestPodDeployDiskFlags: a pod entry may not set the disk bounds
// deploy takes from the pod (task df4), in any spelling; nothing is
// contacted. -disk-min-free still passes.
func TestPodDeployDiskFlags(t *testing.T) {
	for flags, want := range map[string]string{
		`"-disk-limit=0"`:                     "flag -disk-limit=0: deploy sets it",
		`"--disk-max-bytes", "1GiB"`:          "flag --disk-max-bytes: deploy sets it",
		`"-duration", "1h", "-disk-limit=50"`: "flag -disk-limit=50: deploy sets it",
		`"-disk-min-free", "1GiB"`:            "",
	} {
		f := newPodFixture(t, strings.Replace(podRegistry, `flags: ["-duration", "1h"]`, "flags: ["+flags+"]", 1))
		f.pod(t, podObject("checkout-0", uid(1), false))
		code := f.run("deploy", "kind/shop/checkout-0")
		if want == "" {
			if code != exitOK {
				t.Errorf("%s: exit %d: %s", flags, code, f.stderr)
			}
			continue
		}
		if code != exitError || !strings.Contains(f.stderr.String(), want) || len(f.calls(t)) != 0 {
			t.Errorf("%s: exit %d, calls %q, stderr %s; want %q", flags, code, f.calls(t), f.stderr, want)
		}
	}
}

// TestPodDeployRefusals: an unprepared pod is refused naming the prepare
// patch, a registry without any image is refused (no tracer image) — and
// neither ever runs kubectl debug. Collect still works without an image.
func TestPodDeployRefusals(t *testing.T) {
	f := newPodFixture(t, "")
	obj := podObject("checkout-0", uid(1), false)
	delete(obj["spec"].(map[string]any), "volumes")
	f.pod(t, obj)
	if code := f.run("deploy", "kind/shop/checkout-0"); code != exitError ||
		!strings.Contains(f.stderr.String(), k8s.PreparePatch) {
		t.Errorf("unprepared: exit %d, stderr %s", code, f.stderr)
	}
	if got := verbs(f.calls(t)); slices.Contains(got, "debug checkout-0") {
		t.Errorf("unprepared pod debugged: %q", got)
	}
	f = newPodFixture(t, strings.Replace(podRegistry, "  image: "+podImage+"\n", "", 1))
	f.pod(t, podObject("checkout-0", uid(1), false))
	if code := f.run("deploy", "kind/shop/checkout-0"); code != exitError ||
		!strings.Contains(f.stderr.String(), "no tracer image configured") {
		t.Errorf("no image: exit %d, stderr %s", code, f.stderr)
	}
	if got := verbs(f.calls(t)); slices.Contains(got, "debug checkout-0") {
		t.Errorf("debugged without an image: %q", got)
	}
	if code := f.run("collect", "kind/shop/checkout-0"); code != exitOK {
		t.Errorf("collect without an image: exit %d: %s", code, f.stderr)
	}
}

// TestPodRm: rm collects the entry one last time, then drops it, saying
// the tracer is not stopped; a collect that fails leaves the registry
// unchanged, unless -force.
func TestPodRm(t *testing.T) {
	f := newPodFixture(t, "")
	f.pod(t, podObject("checkout-0", uid(1), true))
	f.file(t, "down-checkout-0", nil)
	if code := f.run("rm", "kind/shop/checkout-0"); code != exitError ||
		!strings.Contains(f.stderr.String(), "registry unchanged") {
		t.Fatalf("unreachable: exit %d, stderr %s", code, f.stderr)
	}
	assertPodEntries(t, f.registry, 2)
	if code := f.run("rm", "-force", "kind/shop/checkout-0"); code != exitOK ||
		!strings.Contains(f.stderr.String(), "-force: dropping the entry") {
		t.Fatalf("-force: exit %d, stderr %s", code, f.stderr)
	}
	assertPodEntries(t, f.registry, 1)
	f.list(t, podObject("web-a", uid(2), true))
	if code := f.run("rm", "kind/shop/app=web"); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr)
	}
	if got := verbs(f.calls(t)); !slices.Equal(got, []string{"get pods", "exec web-a"}) {
		t.Errorf("calls %q", got)
	}
	if !strings.Contains(f.stdout.String(), "is not stopped") {
		t.Errorf("stdout: %s", f.stdout)
	}
	assertPodEntries(t, f.registry, 0)
}

// TestPinNewHostsRereads: pinNewHosts merges into the file as it is now,
// under its lock, so a pin another process (a collect, a build) wrote
// since the caller loaded the pins survives (review N2), and an existing
// pin is never overwritten.
func TestPinNewHostsRereads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "share", hostPinsFileName)
	a := federate.HostIdentity{HostID: "pod-" + uid(1), FQDN: "a.shop.0123abcd"}
	b := federate.HostIdentity{HostID: "pod-" + uid(2), FQDN: "b.shop.0123abcd"}
	if err := pinNewHosts(path, map[string]federate.HostIdentity{"pod-" + uid(1): a}); err != nil {
		t.Fatal(err)
	}
	if err := pinNewHosts(path, map[string]federate.HostIdentity{"pod-" + uid(2): b,
		"pod-" + uid(1): {HostID: "forged"}}); err != nil {
		t.Fatal(err)
	}
	pins, err := loadHostPins(path)
	if err != nil || len(pins) != 2 || pins["pod-"+uid(1)] != a || pins["pod-"+uid(2)] != b {
		t.Errorf("pins %v, %v", pins, err)
	}
}

// assertPodEntries checks how many pods: entries the registry holds.
func assertPodEntries(t *testing.T, path string, n int) {
	t.Helper()
	r, err := federate.LoadRegistry(path)
	if err != nil || len(r.Pods) != n {
		t.Errorf("registry: %d pod entries, %v; want %d", len(r.Pods), err, n)
	}
}

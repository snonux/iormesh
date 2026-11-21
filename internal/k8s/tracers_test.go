package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// Tests for a debug pod's successive tracers (task ah4): deploy replaces
// an ended debug tracer with the next TracerName, leaves a running one
// alone, stops at MaxDebugTracers, and every exec after it names the
// newest one.

// tracerSpec is one ephemeral container of a test pod: its name, its
// spec image and its state as the status JSON writes it.
type tracerSpec struct{ name, image, state string }

// The states a test tracer can be in.
const (
	stRunning = `{"running":{"startedAt":"2026-11-21T00:00:00Z"}}`
	stKilled  = `{"terminated":{"reason":"Error","exitCode":137}}`
	stWaiting = `{"waiting":{"reason":"ContainerCreating"}}`
)

// pinnedID is the status imageID of a tracer running testImage.
var pinnedID = "registry.example/iormesh@sha256:" + testDigest

// podWith is preparedPod with the ephemeral containers ts, each with a
// status of its state ("" leaves its status out), imageID the pinned
// digest.
func podWith(t *testing.T, ts ...tracerSpec) string {
	t.Helper()
	var spec, status []map[string]any
	for _, c := range ts {
		spec = append(spec, map[string]any{"name": c.name, "image": c.image,
			"command": []string{"iormesh", "run", "-project", "shop"}})
		if c.state == "" {
			continue
		}
		status = append(status, map[string]any{"name": c.name, "imageID": pinnedID,
			"state": json.RawMessage(c.state)})
	}
	var pod map[string]any
	if err := json.Unmarshal([]byte(preparedPod), &pod); err != nil {
		t.Fatal(err)
	}
	pod["spec"].(map[string]any)["ephemeralContainers"] = spec
	pod["status"] = map[string]any{"ephemeralContainerStatuses": status}
	b, err := json.Marshal(pod)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// argValue is the value of --name=V in argv, "" without it.
func argValue(argv []string, name string) string {
	for _, a := range argv {
		if v, ok := strings.CutPrefix(a, "--"+name+"="); ok {
			return v
		}
	}
	return ""
}

func TestTracerName(t *testing.T) {
	for n := 1; n <= MaxDebugTracers; n++ {
		if got, ok := tracerIndex(TracerName(n)); !ok || got != n {
			t.Errorf("tracerIndex(TracerName(%d) = %q) = %d, %v", n, TracerName(n), got, ok)
		}
	}
	if TracerName(1) != "iormesh" || TracerName(2) != "iormesh-2" || TracerName(MaxDebugTracers) != "iormesh-8" {
		t.Errorf("names %q %q %q", TracerName(1), TracerName(2), TracerName(MaxDebugTracers))
	}
	for _, bad := range []string{"", "iormesh-", "iormesh-1", "iormesh-0", "iormesh-02", "iormesh-9",
		"iormesh-10", "iormesh-+3", "iormesh--3", "iormesh-3a", "iormesh3", "Iormesh", "app"} {
		if n, ok := tracerIndex(bad); ok {
			t.Errorf("tracerIndex(%q) = %d, true; want refused", bad, n)
		}
	}
}

// TestDeployReplacesEnded: a pod whose debug tracers all terminated (the
// app container's restart killed the newest, exit 137) gets a fresh one
// under the next name — the same get and debug as a first deploy, the
// pinned --image, the tracer argv ended by the pod's disk bounds — and
// the transport names it from then on. Containers listed out of order
// are ordered by their number, and names that are not tracer names are
// not counted.
func TestDeployReplacesEnded(t *testing.T) {
	for _, tc := range []struct {
		pod  []tracerSpec
		want string
	}{
		{[]tracerSpec{{"iormesh", testImage, stKilled}}, "iormesh-2"},
		{[]tracerSpec{{"iormesh-2", testImage, stKilled}, {"iormesh", testImage, stKilled}}, "iormesh-3"},
		{[]tracerSpec{{"iormesh", testImage, stKilled}, {"iormesh-02", testImage, stRunning},
			{"iormesh-9", testImage, stRunning}}, "iormesh-2"},
		{[]tracerSpec{{"iormesh", testImage, stKilled}, {"iormesh-7", testImage, stKilled}}, "iormesh-8"},
	} {
		f := newFake(t)
		f.pod(t, podWith(t, tc.pod...))
		k := newTestKubectl(t, testTarget())
		out, err := k.Deploy(context.Background(), "iormesh", "run", "-project", "shop")
		if err != nil || out != Replaced || k.Tracer() != tc.want {
			t.Fatalf("%v: Deploy = %v, %v, tracer %q; want Replaced, %q", tc.pod, out, err, k.Tracer(), tc.want)
		}
		calls := f.calls(t)
		tail := []string{"--", "iormesh", "run", "-project", "shop", "-disk-limit", "0", "-disk-max-bytes", "1536MiB"}
		if len(calls) != 2 || calls[0][2] != "get" || calls[1][2] != "debug" ||
			argValue(calls[1], "container") != tc.want || argValue(calls[1], "image") != testImage ||
			argValue(calls[1], "target") != "app" || !slices.Equal(calls[1][len(calls[1])-len(tail):], tail) {
			t.Errorf("%v: calls = %q", tc.pod, calls)
		}
	}
}

// TestDeployKeepsNewestRunning: a newest tracer that runs is left alone
// whatever ended before it — no debug call — and the transport names it;
// the pinning checks (af4, N6) and the project check (5g4) apply to it
// as to a first tracer.
func TestDeployKeepsNewestRunning(t *testing.T) {
	other := "registry.example/iormesh@sha256:" + strings.Repeat("f", 64)
	f := newFake(t)
	f.pod(t, podWith(t, tracerSpec{"iormesh", testImage, stKilled}, tracerSpec{"iormesh-2", testImage, stRunning}))
	k := newTestKubectl(t, testTarget())
	out, err := k.Deploy(context.Background(), "iormesh", "run", "-project", "shop")
	if err != nil || out != Running || k.Tracer() != "iormesh-2" {
		t.Fatalf("Deploy = %v, %v, tracer %q", out, err, k.Tracer())
	}
	if calls := f.calls(t); len(calls) != 1 {
		t.Errorf("calls = %q, want the get alone", calls)
	}
	for name, c := range map[string]struct {
		pod  string
		want error
		argv []string
	}{
		"other spec image": {podWith(t, tracerSpec{"iormesh", testImage, stKilled},
			tracerSpec{"iormesh-2", other, stRunning}), ErrImage, nil},
		"other project": {podWith(t, tracerSpec{"iormesh", testImage, stKilled},
			tracerSpec{"iormesh-2", testImage, stRunning}), ErrTransport, []string{"-project", "cart"}},
		"other imageID": {strings.Replace(podWith(t, tracerSpec{"iormesh", testImage, stKilled},
			tracerSpec{"iormesh-2", testImage, stRunning}), `"imageID":"`+pinnedID+`","name":"iormesh-2"`,
			`"imageID":"`+other+`","name":"iormesh-2"`, 1), ErrImage, nil},
	} {
		f := newFake(t)
		f.pod(t, c.pod)
		_, err := newTestKubectl(t, testTarget()).Deploy(context.Background(), append([]string{"iormesh", "run"},
			c.argv...)...)
		if !errors.Is(err, c.want) || !strings.Contains(err.Error(), `"iormesh-2"`) {
			t.Errorf("%s: err = %v, want %v naming iormesh-2", name, err, c.want)
		}
		if calls := f.calls(t); len(calls) != 1 {
			t.Errorf("%s: calls = %q, want the get alone", name, calls)
		}
	}
}

// TestDeployReplaceRefused: no fresh tracer is started while the newest
// has not terminated (still pulling, or no status yet), while an
// earlier one has not (one tracer per pod), or once the pod has had
// MaxDebugTracers; each refusal names why, and only the get runs.
func TestDeployReplaceRefused(t *testing.T) {
	var full []tracerSpec
	for n := 1; n <= MaxDebugTracers; n++ {
		full = append(full, tracerSpec{TracerName(n), testImage, stKilled})
	}
	for name, c := range map[string]struct {
		pod  []tracerSpec
		want string
	}{
		"cap reached": {full, "the pod has had 8, the most deploy starts"},
		"newest waiting": {[]tracerSpec{{"iormesh", testImage, stKilled}, {"iormesh-2", testImage, stWaiting}},
			"waiting (ContainerCreating); a debug tracer that has not ended cannot be replaced"},
		"newest without status": {[]tracerSpec{{"iormesh", testImage, stKilled}, {"iormesh-2", testImage, ""}},
			"no status yet"},
		"earlier running": {[]tracerSpec{{"iormesh", testImage, stRunning}, {"iormesh-2", testImage, stKilled}},
			`debug tracer "iormesh" is running beside the newer "iormesh-2"`},
		"earlier without status": {[]tracerSpec{{"iormesh", testImage, ""}, {"iormesh-2", testImage, stKilled}},
			`"iormesh" is without a status yet beside`},
	} {
		f := newFake(t)
		f.pod(t, podWith(t, c.pod...))
		_, err := newTestKubectl(t, testTarget()).Deploy(context.Background(), "iormesh", "run", "-project", "shop")
		if !errors.Is(err, ErrTransport) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want ErrTransport with %q", name, err, c.want)
		}
		if calls := f.calls(t); len(calls) != 1 || calls[0][2] != "get" {
			t.Errorf("%s: calls = %q, want the get alone", name, calls)
		}
	}
}

// TestWaitAttachedAfterReplace: after a replacement the probe execs into
// the new tracer, and the predecessor's state.json — dead, or ended if
// it was killed while lingering — is waited past until the new run
// says running. Without a replacement the same answers fail at once
// (TestWaitAttachedEnded).
func TestWaitAttachedAfterReplace(t *testing.T) {
	fastPoll(t)
	for _, first := range []string{"dead", "ended duration"} {
		f := newFake(t)
		f.pod(t, podWith(t, tracerSpec{"iormesh", testImage, stKilled}))
		k := newTestKubectl(t, testTarget())
		if out, err := k.Deploy(context.Background(), "iormesh", "run", "-project", "shop"); err != nil || out != Replaced {
			t.Fatalf("Deploy = %v, %v", out, err)
		}
		f.states(t, first, first, "running 1a2b3c4d")
		session, err := k.WaitAttached(context.Background(), "shop", time.Minute)
		if err != nil || session != "1a2b3c4d" {
			t.Fatalf("%s: WaitAttached = %q, %v", first, session, err)
		}
		calls := f.calls(t)[2:] // after the get and the debug
		if len(calls) != 3 || argValue(calls[0], "container") != "iormesh-2" {
			t.Errorf("%s: probes = %q, want three into iormesh-2", first, calls)
		}
	}
	// A predecessor's state that never gives way times out naming it.
	f := newFake(t)
	f.pod(t, podWith(t, tracerSpec{"iormesh", testImage, stKilled}))
	k := newTestKubectl(t, testTarget())
	if _, err := k.Deploy(context.Background(), "iormesh", "run"); err != nil {
		t.Fatal(err)
	}
	f.states(t, "dead")
	_, err := k.WaitAttached(context.Background(), "shop", 100*time.Millisecond)
	if !errors.Is(err, ErrNotAttached) || !strings.Contains(err.Error(), "the ended tracer's state (dead)") ||
		!strings.Contains(err.Error(), "--container=iormesh-2") {
		t.Errorf("timeout: err = %v", err)
	}
}

// TestWaitAttachedReplacedExits: a replacement tracer whose own run
// dies takes its container down with it, which the probe reads from the
// pod's status of that container and fails on at once.
func TestWaitAttachedReplacedExits(t *testing.T) {
	fastPoll(t)
	f := newFake(t)
	f.pod(t, podWith(t, tracerSpec{"iormesh", testImage, stKilled}))
	k := newTestKubectl(t, testTarget())
	if _, err := k.Deploy(context.Background(), "iormesh", "run"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_KUBECTL_EXEC_DOWN", "1")
	f.pod(t, podWith(t, tracerSpec{"iormesh", testImage, stKilled}, tracerSpec{"iormesh-2", testImage,
		`{"terminated":{"reason":"Error","exitCode":1}}`}))
	_, err := k.WaitAttached(context.Background(), "shop", time.Minute)
	if !errors.Is(err, ErrNotAttached) || !strings.Contains(err.Error(), `"iormesh-2" terminated (Error, exit code 1)`) {
		t.Errorf("err = %v", err)
	}
}

// TestPodStateNewestTracer: the lister reports the newest debug tracer
// as the pod's tracer container, stopped or not by its own status
// alone, and ignores names that are not tracer names.
func TestPodStateNewestTracer(t *testing.T) {
	for _, c := range []struct {
		pod           []tracerSpec
		name, stopped string
	}{
		{[]tracerSpec{{"iormesh", testImage, stKilled}, {"iormesh-2", testImage, stRunning}}, "iormesh-2", ""},
		{[]tracerSpec{{"iormesh-3", testImage, stKilled}, {"iormesh", testImage, stRunning}}, "iormesh-3",
			"terminated (Error, exit code 137)"},
		{[]tracerSpec{{"iormesh", testImage, stRunning}, {"iormesh-9", testImage, stKilled}}, "iormesh", ""},
		{[]tracerSpec{{"iormesh-02", testImage, stRunning}}, "", ""},
	} {
		f := newFake(t)
		var pod map[string]any
		if err := json.Unmarshal([]byte(podWith(t, c.pod...)), &pod); err != nil {
			t.Fatal(err)
		}
		pod["status"].(map[string]any)["phase"] = "Running"
		b, _ := json.Marshal(pod)
		f.pod(t, string(b))
		st, err := newTestLister(t).Get(context.Background(), "checkout-7d9f")
		if err != nil || st.Container != c.name || st.Tracer != (c.name != "") || st.Stopped != c.stopped {
			t.Errorf("%v: state %+v, %v; want container %q stopped %q", c.pod, st, err, c.name, c.stopped)
		}
	}
}

// TestExecNamesTracer: Run and Fetch exec into the target's Tracer, and
// a tracer name that is not one TracerName makes — or any but
// TracerContainer for a sidecar — is refused, as is an app container
// named like a tracer.
func TestExecNamesTracer(t *testing.T) {
	f := newFake(t)
	tg := testTarget()
	tg.Tracer = "iormesh-3"
	k := newTestKubectl(t, tg)
	if _, err := k.Run(context.Background(), "iormesh", "version"); err != nil {
		t.Fatal(err)
	}
	f.tar(t, nil)
	if err := k.Fetch(context.Background(), TraceRoot+"/shop/ready", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls(t) {
		if argValue(c, "container") != "iormesh-3" {
			t.Errorf("call %q does not name iormesh-3", c)
		}
	}
	for _, bad := range []func(*Target){
		func(t *Target) { t.Tracer = "iormesh-9" },
		func(t *Target) { t.Tracer = "iormesh-02" },
		func(t *Target) { t.Tracer = "app" },
		func(t *Target) { t.Mode, t.Tracer = ModeSidecar, "iormesh-2" },
		func(t *Target) { t.Container = "iormesh-4" },
	} {
		tg := testTarget()
		bad(&tg)
		if _, err := New(tg); !errors.Is(err, ErrName) {
			t.Errorf("%+v: New err = %v, want ErrName", tg, err)
		}
	}
	sidecar := testTarget()
	sidecar.Mode, sidecar.Tracer = ModeSidecar, TracerContainer
	if _, err := New(sidecar); err != nil {
		t.Errorf("sidecar named %q: %v", TracerContainer, err)
	}
}

package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// TestRunArgv: the context and namespace are explicit on every call, in
// --flag=value form, and the remote argv follows "--" element by element
// — shell metacharacters arrive verbatim, as separate arguments, since
// neither kubectl nor the runtime runs a shell.
func TestRunArgv(t *testing.T) {
	f := newFake(t)
	k := newTestKubectl(t, testTarget())
	argv := []string{"iormesh", "status", "-project", "shop", "$(id)", "a;b", "it's", "two words"}
	out, err := k.Run(context.Background(), argv...)
	if err != nil || string(out) != "ran\n" {
		t.Fatalf("Run = %q, %v", out, err)
	}
	want := slices.Concat([]string{"--context=kind-iormesh", "--namespace=shop",
		"exec", "checkout-7d9f", "--container=iormesh", "--"}, argv)
	if calls := f.calls(t); len(calls) != 1 || !slices.Equal(calls[0], want) {
		t.Errorf("argv\n got %q\nwant %q", calls, want)
	}
}

// TestRunExitStatus: the remote status comes back as *exec.ExitError,
// with kubectl's stderr in the message, as the Transport contract asks.
func TestRunExitStatus(t *testing.T) {
	newFake(t)
	t.Setenv("FAKE_KUBECTL_EXIT", "1")
	k := newTestKubectl(t, testTarget())
	_, err := k.Run(context.Background(), "test", "-d", "/opt/iormesh/traces/shop/ready")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("err = %v, want exit status 1", err)
	}
	if !strings.Contains(err.Error(), "fake kubectl stderr") {
		t.Errorf("err %q lacks kubectl's stderr", err)
	}
	if _, err := k.Run(context.Background()); !errors.Is(err, ErrTransport) {
		t.Errorf("empty argv: %v", err)
	}
}

// TestNewRefusesInjection: a pod or container name carrying shell or
// option syntax is refused before any kubectl runs.
func TestNewRefusesInjection(t *testing.T) {
	f := newFake(t)
	for _, n := range []string{"x;touch /tmp/pwned", "$(id)", "--all-namespaces", "-it", "a b"} {
		tg := testTarget()
		tg.Pod = n
		if _, err := New(tg); !errors.Is(err, ErrName) {
			t.Errorf("pod %q: err = %v", n, err)
		}
		tg = testTarget()
		tg.Container = n
		if _, err := New(tg); !errors.Is(err, ErrName) {
			t.Errorf("container %q: err = %v", n, err)
		}
	}
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("kubectl ran: %q", calls)
	}
}

// TestPushRefused: the image carries the tracer; nothing is copied in.
func TestPushRefused(t *testing.T) {
	f := newFake(t)
	k := newTestKubectl(t, testTarget())
	if err := k.Push(context.Background(), "stage", "/bin/true"); !errors.Is(err, ErrTransport) {
		t.Errorf("Push: %v", err)
	}
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("kubectl ran: %q", calls)
	}
}

// preparedPod is a pod the prepare patch has run on.
const preparedPod = `{"spec":{
 "volumes":[{"name":"tracefs","hostPath":{"path":"/sys/kernel/tracing","type":"Directory"}},
            {"name":"iormesh-traces","emptyDir":{"sizeLimit":"2Gi"}}],
 "containers":[{"name":"app"}]}}`

// TestDebugArgv: on a prepared pod, Check reads the spec, then one
// `kubectl debug` with the target container, the pinned image, the
// profile and the --custom file — and the tracer argv after "--".
func TestDebugArgv(t *testing.T) {
	f := newFake(t)
	f.pod(t, preparedPod)
	k := newTestKubectl(t, testTarget())
	argv := []string{"iormesh", "run", "-pattern", "java*", "-project", "shop"}
	if err := k.Debug(context.Background(), argv...); err != nil {
		t.Fatal(err)
	}
	calls := f.calls(t)
	if len(calls) != 2 {
		t.Fatalf("calls = %q", calls)
	}
	wantGet := []string{"--context=kind-iormesh", "--namespace=shop", "get", "pod", "checkout-7d9f", "--output=json"}
	if !slices.Equal(calls[0], wantGet) {
		t.Errorf("get argv\n got %q\nwant %q", calls[0], wantGet)
	}
	dbg := calls[1]
	i := slices.IndexFunc(dbg, func(a string) bool { return strings.HasPrefix(a, "--custom=") })
	if i < 0 {
		t.Fatalf("no --custom in %q", dbg)
	}
	want := slices.Concat([]string{"--context=kind-iormesh", "--namespace=shop", "debug", "checkout-7d9f",
		"--container=iormesh", "--target=app", "--image=" + testImage, "--attach=false",
		"--profile=general", dbg[i], "--"}, argv)
	if !slices.Equal(dbg, want) {
		t.Errorf("debug argv\n got %q\nwant %q", dbg, want)
	}
	assertCustom(t, f.custom(t), true)
}

// TestDebugPrivileged: the fallback profile is sysadmin, and its custom
// file carries the mounts but no allowPrivilegeEscalation: false (the
// API refuses it beside privileged).
func TestDebugPrivileged(t *testing.T) {
	f := newFake(t)
	f.pod(t, preparedPod)
	tg := testTarget()
	tg.Profile = ProfilePrivileged
	if err := newTestKubectl(t, tg).Debug(context.Background(), "iormesh", "run"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.calls(t)[1], "--profile=sysadmin") {
		t.Errorf("argv %q lacks --profile=sysadmin", f.calls(t)[1])
	}
	assertCustom(t, f.custom(t), false)
}

// assertCustom checks the --custom spec: the two mounts always, #13's
// capability set and no-escalation only for the minimal profile.
func assertCustom(t *testing.T, js string, minimal bool) {
	t.Helper()
	var spec struct {
		SecurityContext struct {
			RunAsUser                *int  `json:"runAsUser"`
			AllowPrivilegeEscalation *bool `json:"allowPrivilegeEscalation"`
			Capabilities             *struct {
				Drop []string `json:"drop"`
				Add  []string `json:"add"`
			} `json:"capabilities"`
		} `json:"securityContext"`
		VolumeMounts []struct {
			Name, MountPath string
			ReadOnly        bool
		} `json:"volumeMounts"`
	}
	if err := json.Unmarshal([]byte(js), &spec); err != nil {
		t.Fatalf("custom %q: %v", js, err)
	}
	sc := spec.SecurityContext
	if sc.RunAsUser == nil || *sc.RunAsUser != 0 {
		t.Errorf("runAsUser in %s", js)
	}
	if len(spec.VolumeMounts) != 2 || spec.VolumeMounts[0].MountPath != TracefsPath ||
		!spec.VolumeMounts[0].ReadOnly || spec.VolumeMounts[1].MountPath != TraceRoot {
		t.Errorf("volume mounts in %s", js)
	}
	if !minimal {
		if sc.AllowPrivilegeEscalation != nil || sc.Capabilities != nil {
			t.Errorf("privileged custom carries the minimal set: %s", js)
		}
		return
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation || sc.Capabilities == nil ||
		!slices.Equal(sc.Capabilities.Drop, []string{"ALL"}) || !slices.Equal(sc.Capabilities.Add, minimalCaps) {
		t.Errorf("minimal security context in %s", js)
	}
}

// TestDebugRefusesUnprepared: a pod without #13's volumes (or with them
// of the wrong kind), a user-namespaced pod, an unknown app container
// and a pod already carrying the debug container are refused — only the
// `get` ran, never a `debug`.
func TestDebugRefusesUnprepared(t *testing.T) {
	cases := map[string]struct {
		pod  string
		want error
	}{
		"no volumes": {`{"spec":{"containers":[{"name":"app"}]}}`, ErrUnprepared},
		"no traces": {`{"spec":{"volumes":[{"name":"tracefs","hostPath":{"path":"/sys/kernel/tracing"}}],
			"containers":[{"name":"app"}]}}`, ErrUnprepared},
		"wrong tracefs path": {strings.Replace(preparedPod, `"/sys/kernel/tracing"`, `"/"`, 1), ErrUnprepared},
		"unbounded emptyDir": {strings.Replace(preparedPod, `{"sizeLimit":"2Gi"}`, `{}`, 1), ErrUnprepared},
		"traces on hostPath": {strings.Replace(preparedPod, `"emptyDir":{"sizeLimit":"2Gi"}`,
			`"hostPath":{"path":"/var"}`, 1), ErrUnprepared},
		"user namespace":   {strings.Replace(preparedPod, `"spec":{`, `"spec":{"hostUsers":false,`, 1), ErrUnprepared},
		"no app container": {strings.Replace(preparedPod, `"name":"app"`, `"name":"web"`, 1), ErrName},
		"already debugged": {strings.Replace(preparedPod, `"containers"`,
			`"ephemeralContainers":[{"name":"iormesh"}],"containers"`, 1), ErrTransport},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.pod(t, c.pod)
			err := newTestKubectl(t, testTarget()).Debug(context.Background(), "iormesh", "run")
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if c.want == ErrUnprepared && !strings.Contains(err.Error(), PreparePatch) {
				t.Errorf("err %q does not name %s", err, PreparePatch)
			}
			if calls := f.calls(t); len(calls) != 1 || calls[0][2] != "get" {
				t.Errorf("calls = %q, want the get alone", calls)
			}
		})
	}
}

// TestSidecarMode: Debug is refused, Check wants the tracer container and
// a shared process namespace.
func TestSidecarMode(t *testing.T) {
	f := newFake(t)
	tg := testTarget()
	tg.Mode = ModeSidecar
	k := newTestKubectl(t, tg)
	if err := k.Debug(context.Background(), "iormesh"); !errors.Is(err, ErrTransport) {
		t.Errorf("Debug in sidecar mode: %v", err)
	}
	f.pod(t, preparedPod)
	if err := k.Check(context.Background()); !errors.Is(err, ErrUnprepared) {
		t.Errorf("sidecar without tracer container: %v", err)
	}
	f.pod(t, strings.Replace(preparedPod, `"containers":[{"name":"app"}]`,
		`"shareProcessNamespace":true,"containers":[{"name":"app"},{"name":"iormesh"}]`, 1))
	if err := k.Check(context.Background()); err != nil {
		t.Errorf("prepared sidecar pod: %v", err)
	}
}

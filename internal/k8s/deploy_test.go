package k8s

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// testDigest is testImage's digest.
var testDigest = testImage[strings.Index(testImage, "@sha256:")+len("@sha256:"):]

// withTracer returns preparedPod with a tracer container — ephemeral, or
// a sidecar with shareProcessNamespace — whose status is running (or not)
// with imageID.
func withTracer(sidecar, running bool, imageID string) string {
	state := `{"terminated":{"exitCode":0}}`
	if running {
		state = `{"running":{"startedAt":"2026-11-21T00:00:00Z"}}`
	}
	status := `[{"name":"iormesh","imageID":"` + imageID + `","state":` + state + `}]`
	if sidecar {
		pod := strings.Replace(preparedPod, `"containers":[{"name":"app"}]`,
			`"shareProcessNamespace":true,"containers":[{"name":"app"},{"name":"iormesh"}]`, 1)
		return strings.TrimSuffix(pod, "}") + `,"status":{"containerStatuses":` + status + `}}`
	}
	pod := strings.Replace(preparedPod, `"containers"`,
		`"ephemeralContainers":[{"name":"iormesh","image":"`+testImage+`"}],"containers"`, 1)
	return strings.TrimSuffix(pod, "}") + `,"status":{"ephemeralContainerStatuses":` + status + `}}`
}

// TestDeployStarts: a prepared pod without a tracer gets the debug
// container — the same get and debug argv as Debug.
func TestDeployStarts(t *testing.T) {
	f := newFake(t)
	f.pod(t, preparedPod)
	out, err := newTestKubectl(t, testTarget()).Deploy(context.Background(), "iormesh", "run", "-project", "shop")
	if err != nil || out != Started {
		t.Fatalf("Deploy = %v, %v", out, err)
	}
	calls := f.calls(t)
	if len(calls) != 2 || calls[0][2] != "get" || calls[1][2] != "debug" ||
		!slices.Equal(calls[1][len(calls[1])-4:], []string{"iormesh", "run", "-project", "shop"}) {
		t.Errorf("calls = %q", calls)
	}
}

// TestDeployLeavesRunningTracer: a pod whose ephemeral tracer runs the
// pinned digest (imageID as containerd and cri-dockerd write it) is left
// alone — no debug call.
func TestDeployLeavesRunningTracer(t *testing.T) {
	for _, id := range []string{"registry.example/iormesh@sha256:" + testDigest,
		"docker-pullable://registry.example/iormesh@sha256:" + testDigest} {
		f := newFake(t)
		f.pod(t, withTracer(false, true, id))
		out, err := newTestKubectl(t, testTarget()).Deploy(context.Background(), "iormesh", "run")
		if err != nil || out != Running {
			t.Fatalf("imageID %q: Deploy = %v, %v", id, out, err)
		}
		if calls := f.calls(t); len(calls) != 1 {
			t.Errorf("calls = %q, want the get alone", calls)
		}
	}
}

// TestDeploySidecar: sidecar mode only verifies — the sidecar running the
// pinned digest passes, nothing is started.
func TestDeploySidecar(t *testing.T) {
	f := newFake(t)
	f.pod(t, withTracer(true, true, "registry.example/iormesh@sha256:"+testDigest))
	tg := testTarget()
	tg.Mode = ModeSidecar
	out, err := newTestKubectl(t, tg).Deploy(context.Background())
	if err != nil || out != SidecarVerified {
		t.Fatalf("Deploy = %v, %v", out, err)
	}
	if calls := f.calls(t); len(calls) != 1 {
		t.Errorf("calls = %q, want the get alone", calls)
	}
}

// TestDeployRefusesUnpinned: a tracer container — sidecar or an earlier
// ephemeral one — running another digest, an imageID naming no registry
// digest (a locally loaded image), one not running, or a sidecar pod
// without the sidecar is refused; nothing is started.
func TestDeployRefusesUnpinned(t *testing.T) {
	other := "registry.example/iormesh@sha256:" + strings.Repeat("f", 64)
	cases := map[string]struct {
		pod     string
		sidecar bool
		want    error
	}{
		"ephemeral other digest": {withTracer(false, true, other), false, ErrImage},
		"ephemeral spec image other": {strings.Replace(withTracer(false, true,
			"registry.example/iormesh@sha256:"+testDigest), `"image":"`+testImage, `"image":"`+other, 1), false, ErrImage},
		"ephemeral spec image tag": {strings.Replace(withTracer(false, true,
			"registry.example/iormesh@sha256:"+testDigest), `"image":"`+testImage,
			`"image":"registry.example/iormesh:latest`, 1), false, ErrImage},
		"ephemeral local image": {withTracer(false, true, "sha256:"+testDigest), false, ErrImage},
		"ephemeral ended":       {withTracer(false, false, "registry.example/iormesh@sha256:"+testDigest), false, ErrTransport},
		"sidecar other digest":  {withTracer(true, true, other), true, ErrImage},
		"sidecar tag only":      {withTracer(true, true, "registry.example/iormesh:latest"), true, ErrImage},
		"sidecar not running":   {withTracer(true, false, "registry.example/iormesh@sha256:"+testDigest), true, ErrTransport},
		"sidecar missing":       {preparedPod, true, ErrUnprepared},
		"sidecar unprepared":    {`{"spec":{"containers":[{"name":"app"},{"name":"iormesh"}]}}`, true, ErrUnprepared},
		"debug unprepared":      {`{"spec":{"containers":[{"name":"app"}]}}`, false, ErrUnprepared},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.pod(t, c.pod)
			tg := testTarget()
			if c.sidecar {
				tg.Mode = ModeSidecar
			}
			if _, err := newTestKubectl(t, tg).Deploy(context.Background(), "iormesh", "run"); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if calls := f.calls(t); len(calls) != 1 || calls[0][2] != "get" {
				t.Errorf("calls = %q, want the get alone", calls)
			}
		})
	}
}

package k8s

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestResolvePrecedence: flag > pod entry > cluster > k8s: defaults
// (#18), field by field, and the built-in fallbacks for mode and profile.
func TestResolvePrecedence(t *testing.T) {
	other := strings.Replace(testImage, "iormesh@", "other@", 1)
	flag := Layer{Namespace: "flagns"}
	podEntry := Layer{Image: other}
	cluster := Layer{Context: "kind-iormesh", Namespace: "clusterns", Image: testImage}
	defaults := Layer{Context: "prod-ctx", Profile: ProfilePrivileged}
	got, err := Resolve("checkout-7d9f", "app", []string{"kind-iormesh"}, flag, podEntry, cluster, defaults)
	if err != nil {
		t.Fatal(err)
	}
	want := Target{Context: "kind-iormesh", Namespace: "flagns", Pod: "checkout-7d9f", Container: "app",
		Image: other, Mode: ModeDebug, Profile: ProfilePrivileged, SELinuxType: DefaultSELinuxType,
		SELinuxLevel: DefaultSELinuxLevel}
	if got != want {
		t.Errorf("Resolve\n got %+v\nwant %+v", got, want)
	}
}

// TestResolveSELinuxType: an unset type defers to the next layer and
// falls back to spc_t; an empty one set at a higher layer wins and omits
// the field; a malformed one is refused (task 5h4).
func TestResolveSELinuxType(t *testing.T) {
	str := func(s string) *string { return &s }
	base := Layer{Context: "c", Namespace: "n"}
	for _, c := range []struct {
		layers []Layer
		want   string
	}{
		{[]Layer{base}, DefaultSELinuxType},
		{[]Layer{{}, base, {SELinuxType: str("iormesh_container_t")}}, "iormesh_container_t"},
		{[]Layer{{SELinuxType: str("")}, base, {SELinuxType: str("iormesh_container_t")}}, ""},
		{[]Layer{{SELinuxType: str("container_t")}, base, {SELinuxType: str("")}}, "container_t"},
	} {
		got, err := Resolve("p", "app", []string{"c"}, c.layers...)
		if err != nil || got.SELinuxType != c.want {
			t.Errorf("layers %+v: SELinux type %q, err %v; want %q", c.layers, got.SELinuxType, err, c.want)
		}
	}
	for _, bad := range []string{"system_u:system_r:spc_t:s0", "spc-t", "SPC_T", "spc_t\n"} {
		l := base
		l.SELinuxType = str(bad)
		if _, err := Resolve("p", "app", []string{"c"}, l); !errors.Is(err, ErrTransport) {
			t.Errorf("type %q: err = %v, want ErrTransport", bad, err)
		}
	}
}

// TestResolveSELinuxLevel: the level resolves as the type does — unset
// defers and falls back to s0, an empty one set higher up wins and
// omits the level, a pod's pinned pair passes — and anything but s0 or
// s0:cN,cM with N, M <= 1023 is refused (task 8h4).
func TestResolveSELinuxLevel(t *testing.T) {
	base := Layer{Context: "c", Namespace: "n"}
	for _, c := range []struct {
		layers []Layer
		want   string
	}{
		{[]Layer{base}, DefaultSELinuxLevel},
		{[]Layer{{}, base, {SELinuxLevel: new("s0:c19,c249")}}, "s0:c19,c249"},
		{[]Layer{{SELinuxLevel: new("")}, base, {SELinuxLevel: new("s0:c19,c249")}}, ""},
		{[]Layer{{SELinuxLevel: new("s0:c1,c2")}, base, {SELinuxLevel: new("")}}, "s0:c1,c2"},
	} {
		got, err := Resolve("p", "app", []string{"c"}, c.layers...)
		if err != nil || got.SELinuxLevel != c.want {
			t.Errorf("layers %+v: SELinux level %q, err %v; want %q", c.layers, got.SELinuxLevel, err, c.want)
		}
	}
	for _, bad := range []string{"s1", "s0-s0:c0.c1023", "s0:c1024,c1", "s0:c5", "s0:c5,c5", "spc_t:s0", "s0\n"} {
		l := base
		l.SELinuxLevel = new(bad)
		if _, err := Resolve("p", "app", []string{"c"}, l); !errors.Is(err, ErrTransport) {
			t.Errorf("level %q: err = %v, want ErrTransport", bad, err)
		}
		tg := testTarget()
		tg.SELinuxLevel = bad
		if err := tg.Validate(); !errors.Is(err, ErrTransport) {
			t.Errorf("target level %q: err = %v, want ErrTransport", bad, err)
		}
	}
}

// TestResolveRefusesContext: a context the registry does not list is
// refused even when well-formed — the kubeconfig never picks the
// cluster — and a malformed one before that.
func TestResolveRefusesContext(t *testing.T) {
	base := Layer{Namespace: "shop", Image: testImage}
	for _, ctx := range []string{"prod-ctx", "", "-kubeconfig=/tmp/x", "a b", "kind\n", "ctx;id", "$(id)"} {
		l := base
		l.Context = ctx
		_, err := Resolve("checkout-7d9f", "app", []string{"kind-iormesh"}, l)
		if !errors.Is(err, ErrContext) {
			t.Errorf("context %q: err = %v, want ErrContext", ctx, err)
		}
	}
	for _, ctx := range []string{"kind-iormesh", "arn:aws:eks:eu-west-1:1:cluster/x", "ops@prod"} {
		l := base
		l.Context = ctx
		if _, err := Resolve("checkout-7d9f", "app", []string{ctx}, l); err != nil {
			t.Errorf("context %q refused: %v", ctx, err)
		}
	}
}

// TestValidateRefusesUnpinnedImage: a tag alone, a short or upper-case
// digest, or another algorithm is refused. No image at all passes
// Validate — exec and fetch need none — but Debug and Deploy refuse it
// (DefaultImage is empty until task yb4 publishes ours), before any
// kubectl runs.
func TestValidateRefusesUnpinnedImage(t *testing.T) {
	digest := testImage[strings.Index(testImage, "@"):]
	for _, img := range []string{
		"registry.example/iormesh", "registry.example/iormesh:latest",
		"registry.example/iormesh@sha256:0123", "registry.example/iormesh@sha512:" + digest[8:],
		"registry.example/iormesh@" + strings.ToUpper(digest), "-image" + digest,
		"registry.example/ior mesh" + digest, "registry.example/iormesh" + digest + "\n",
	} {
		tg := testTarget()
		tg.Image = img
		if err := tg.Validate(); !errors.Is(err, ErrImage) {
			t.Errorf("image %q: err = %v, want ErrImage", img, err)
		}
	}
	for _, img := range []string{testImage, "localhost:5000/team/iormesh:v1" + digest, "iormesh" + digest} {
		tg := testTarget()
		tg.Image = img
		if err := tg.Validate(); err != nil {
			t.Errorf("image %q refused: %v", img, err)
		}
	}
	f := newFake(t)
	f.pod(t, preparedPod)
	tg, err := Resolve("p", "app", []string{"c"}, Layer{Context: "c", Namespace: "n"})
	if err != nil {
		t.Fatalf("no image anywhere: Resolve = %v, want it to pass (collect needs no image)", err)
	}
	k := newTestKubectl(t, tg)
	if err := k.Debug(context.Background(), "iormesh", "run"); !errors.Is(err, ErrImage) {
		t.Errorf("no image anywhere: Debug = %v, want ErrImage", err)
	}
	if _, err := k.Deploy(context.Background(), "iormesh", "run"); !errors.Is(err, ErrImage) {
		t.Errorf("no image anywhere: Deploy = %v, want ErrImage", err)
	}
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("kubectl ran without an image: %q", calls)
	}
}

// TestValidateRefusesNames: names that are not Kubernetes names never
// reach an argv — shell metacharacters, option-looking names, paths.
func TestValidateRefusesNames(t *testing.T) {
	bad := []string{"", "-oProxyCommand=x", "a b", "a;id", "$(id)", "`id`", "a|b", "../x", "a/b",
		"Upper", "x\n", strings.Repeat("a", 64)}
	for _, n := range bad {
		for _, field := range []string{"namespace", "pod", "container"} {
			tg := testTarget()
			switch field {
			case "namespace":
				tg.Namespace = n
			case "pod":
				tg.Pod = n
			case "container":
				tg.Container = n
			}
			if field == "pod" && n == strings.Repeat("a", 64) {
				continue // a valid pod name (subdomain), too long only for a label
			}
			if err := tg.Validate(); !errors.Is(err, ErrName) {
				t.Errorf("%s %q: err = %v, want ErrName", field, n, err)
			}
		}
	}
	tg := testTarget()
	tg.Container = TracerContainer
	if err := tg.Validate(); !errors.Is(err, ErrName) {
		t.Errorf("app container named %q: err = %v", TracerContainer, err)
	}
}

// TestValidateModeProfile: only the two modes and two profiles.
func TestValidateModeProfile(t *testing.T) {
	tg := testTarget()
	tg.Mode = "hostpid"
	if err := tg.Validate(); !errors.Is(err, ErrTransport) {
		t.Errorf("mode: %v", err)
	}
	tg = testTarget()
	tg.Profile = "sysadmin"
	if err := tg.Validate(); !errors.Is(err, ErrTransport) {
		t.Errorf("profile: %v", err)
	}
}

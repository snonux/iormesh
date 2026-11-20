package k8s

import (
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
		Image: other, Mode: ModeDebug, Profile: ProfilePrivileged}
	if got != want {
		t.Errorf("Resolve\n got %+v\nwant %+v", got, want)
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
// digest, another algorithm, or no image at all (DefaultImage is empty
// until task yb4 publishes ours) is refused.
func TestValidateRefusesUnpinnedImage(t *testing.T) {
	digest := testImage[strings.Index(testImage, "@"):]
	for _, img := range []string{
		"", "registry.example/iormesh", "registry.example/iormesh:latest",
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
	if _, err := Resolve("p", "app", []string{"c"}, Layer{Context: "c", Namespace: "n"}); !errors.Is(err, ErrImage) {
		t.Errorf("no image anywhere: err = %v, want ErrImage", err)
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

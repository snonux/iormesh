package k8sname

import (
	"strings"
	"testing"
)

// digest is a well-formed sha256 digest.
var digest = strings.Repeat("0123456789abcdef", 4)

// TestSelector: equality-based selectors with one positive term pass;
// empty, only-negative, set-based, existence and anything with a space,
// a leading dash or a stray comma are refused.
func TestSelector(t *testing.T) {
	for _, s := range []string{"app=web", "app==web", "app=web,tier!=db", "app.kubernetes.io/name=checkout",
		"app=", "a=b,c=d"} {
		if !Selector(s) {
			t.Errorf("%q refused", s)
		}
	}
	for _, s := range []string{"", "app!=web", "app", "!app", "app in (a,b)", "--all-namespaces", "-l=x",
		"a=b c", "app=web,", ",app=web", "app=-x", "APP/x=y", "a=b;c", strings.Repeat("a=b,", 300) + "a=b"} {
		if Selector(s) {
			t.Errorf("%q accepted", s)
		}
	}
}

// TestDigests: the pinned digest of an image, and the repository digest
// of a status imageID — never of a bare config digest.
func TestDigests(t *testing.T) {
	if got := Digest("registry.example/iormesh@sha256:" + digest); got != digest {
		t.Errorf("Digest = %q", got)
	}
	for _, img := range []string{"registry.example/iormesh:latest", "registry.example/iormesh@sha256:0123", ""} {
		if Digest(img) != "" || Pinned(img) {
			t.Errorf("%q counted as pinned", img)
		}
	}
	for id, want := range map[string]string{
		"registry.example/iormesh@sha256:" + digest:                   digest,
		"docker-pullable://registry.example/iormesh@sha256:" + digest: digest,
		"sha256:" + digest: "",
		"":                 "",
		"registry.example/iormesh@sha256:" + digest + "x":                 "",
		"registry.example/iormesh@sha256:" + strings.ToUpper(digest[:10]): "",
	} {
		if got := StatusDigest(id); got != want {
			t.Errorf("StatusDigest(%q) = %q, want %q", id, got, want)
		}
	}
}

// TestNames: labels, pod names, contexts and UIDs.
func TestNames(t *testing.T) {
	cases := []struct {
		f    func(string) bool
		ok   []string
		bad  []string
		name string
	}{
		{Label, []string{"shop", "a", "kind-1"}, []string{"", "Shop", "-a", "a.b", strings.Repeat("a", 64)}, "Label"},
		{Pod, []string{"checkout-7d9f", "web.0"}, []string{"", "Web", "a b", "../x", "-x"}, "Pod"},
		{Context, []string{"kind-x", "ops@prod", "arn:aws:eks:eu-west-1:1:cluster/x"},
			[]string{"", "-x", "a b", "$(id)"}, "Context"},
		{UID, []string{"0123abcd-0000-4000-8000-000000000001"},
			[]string{"", "../../etc", "0123ABCD-0000-4000-8000-000000000001", "0123abcd00004000800000000000001"}, "UID"},
	}
	for _, c := range cases {
		for _, s := range c.ok {
			if !c.f(s) {
				t.Errorf("%s(%q) = false", c.name, s)
			}
		}
		for _, s := range c.bad {
			if c.f(s) {
				t.Errorf("%s(%q) = true", c.name, s)
			}
		}
	}
}

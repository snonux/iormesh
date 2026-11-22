package k8sname

import (
	"strings"
	"testing"
)

// digest is a well-formed sha256 digest.
var digest = strings.Repeat("0123456789abcdef", 4)

// TestSELinuxType: a bare type name passes; empty, a full context, a
// role, upper case, a dash or a level is refused (task 5h4).
func TestSELinuxType(t *testing.T) {
	for _, s := range []string{"spc_t", "container_t", "iormesh_container_t", "svirt_lxc_net_t", "t0"} {
		if !SELinuxType(s) {
			t.Errorf("%q refused", s)
		}
	}
	for _, s := range []string{"", "system_u:system_r:spc_t:s0", "spc_t:s0", "Spc_t", "spc-t", "spc_t ",
		"-spc_t", "s0:c1,c2", strings.Repeat("a", 129)} {
		if SELinuxType(s) {
			t.Errorf("%q accepted", s)
		}
	}
}

// TestSELinuxLevel: s0 and s0 with a pair of distinct categories up to
// c1023 pass; empty, a range, another sensitivity, a lone or a third
// category, a duplicate, c1024, a leading zero, a full context and stray
// spaces are refused (task 8h4).
func TestSELinuxLevel(t *testing.T) {
	for _, s := range []string{"s0", "s0:c0,c1", "s0:c19,c249", "s0:c1023,c0", "s0:c1022,c1023"} {
		if !SELinuxLevel(s) {
			t.Errorf("%q refused", s)
		}
	}
	for _, s := range []string{"", "s1", "s0-s0", "s0-s0:c0.c1023", "s0:c5", "s0:c1,c2,c3", "s0:c5,c5",
		"s0:c1024,c1", "s0:c1,c1024", "s0:c01,c2", "s0:c1,c02", "s0:c0.c3", "s0:", "S0", " s0", "s0 ",
		"s0\n", "system_u:system_r:spc_t:s0", "spc_t", "s0:c1,c99999", "s0:c,c1"} {
		if SELinuxLevel(s) {
			t.Errorf("%q accepted", s)
		}
	}
}

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
		// Pod names: no per-part cap (a 64-byte part, a 253-byte single
		// part), only the 253-byte total.
		{Pod, []string{"checkout-7d9f", "web.0", strings.Repeat("a", 64), strings.Repeat("a", 64) + ".b",
			strings.Repeat("a", 253), strings.Repeat("a", 126) + "." + strings.Repeat("b", 126)},
			[]string{"", "Web", "a b", "../x", "-x", "a..b", "a-.b", "a.", ".a", strings.Repeat("a", 254),
				strings.Repeat("a", 64) + "-"}, "Pod"},
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

// TestPodNamespace: the namespace comes out of a pod's host identity,
// dotted pod names included, and nothing comes out of a machine host or
// an FQDN that does not end in the host's own UID prefix (task sh4).
func TestPodNamespace(t *testing.T) {
	const uid = "3b1c9a52-7e0d-4c1f-9a8e-2f6d5b4c3a21"
	for _, c := range []struct {
		host, fqdn, want string
		ok               bool
	}{
		{PodHostID(uid), "web-7d9f.prod.3b1c9a52", "prod", true},
		{PodHostID(uid), "web.v2.prod.3b1c9a52", "prod", true},
		{PodHostID(uid), "web.prod.9f9f9f9f", "", false},
		{PodHostID(uid), "prod.3b1c9a52", "", false},
		{PodHostID(uid), "web.Prod.3b1c9a52", "", false},
		{PodHostID("not-a-uid"), "web.prod.not-a-ui", "", false},
		{"0123456789abcdef0123456789abcdef", "web.prod.3b1c9a52", "", false},
	} {
		if got, ok := PodNamespace(c.host, c.fqdn); got != c.want || ok != c.ok {
			t.Errorf("PodNamespace(%q, %q) = %q, %v; want %q, %v", c.host, c.fqdn, got, ok, c.want, c.ok)
		}
	}
}

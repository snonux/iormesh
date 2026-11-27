package acceptance

import (
	"strings"
	"testing"
)

// TestFromEnvRefuses: the run never starts without two explicit,
// distinct, well-formed hosts or with malformed options.
func TestFromEnvRefuses(t *testing.T) {
	cases := []struct {
		h1, h2, why string
		env         map[string]string
	}{
		{"", "h2", "needs two hosts", nil},
		{"h1", "", "needs two hosts", nil},
		{"h1", "u@h1", "same host", nil},
		{"H1", "h1", "same host", nil},
		{"-oProxyCommand=x", "h2", "want [user@]name", nil},
		{"h1", "h2", "IPv4", map[string]string{EnvAddr2: "::1"}},
		{"h1", "h2", "integer in", map[string]string{EnvNATPort: "port"}},
		{"h1", "h2", "integer in", map[string]string{EnvRounds: "1"}},
		{"h1", "h2", "collides", map[string]string{EnvNATPort: "18203"}},
	}
	for _, c := range cases {
		_, err := FromEnv(c.h1, c.h2, "/home/op", func(k string) string { return c.env[k] })
		if err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("FromEnv(%q, %q, %v) = %v, want %q", c.h1, c.h2, c.env, err, c.why)
		}
	}
}

func TestFromEnvDefaults(t *testing.T) {
	c, err := FromEnv("h1", "ops@h2", "/home/op", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if c.WorkDir != "/home/op/.iormesh-m4" || c.Key != "/home/op/.config/iormesh/minisign.pub" ||
		c.Dist != "dist" || c.NATPort != 0 || c.PortBase != 18200 || c.Rounds != 3 {
		t.Errorf("defaults: %+v", c)
	}
	// Without a DNAT port the NAT projects are not registered at all.
	for _, p := range c.projects() {
		if p.name == projNATClient || p.name == projNATServer {
			t.Errorf("NAT project %s without %s", p.name, EnvNATPort)
		}
	}
}

// el9Changelog is the January 2025 entry of el9's rsync package
// (rsync-3.2.7-1.el9_8 carries it), as `rpm -q --changelog` prints it on
// Rocky 9.9: Red Hat names its Jira ids, not the CVEs.
const el9Changelog = `* Wed Jan 29 2025 Michal Ruprich <mruprich@redhat.com> - 3.2.5-2
- Resolves: RHEL-70158 - Info Leak via Uninitialized Stack Contents
- Resolves: RHEL-70208 - Path traversal vulnerability in rsync
- Resolves: RHEL-70210 - --safe-links option bypass leads to path traversal
- Resolves: RHEL-71657 - Race Condition in rsync Handling Symbolic Links
`

// debChangelog is the shape of a Debian security upload's entry.
const debChangelog = `rsync (3.2.7-1+deb12u1) bookworm-security; urgency=medium
  * Fix CVE-2024-12084, CVE-2024-12085, CVE-2024-12086, CVE-2024-12087,
    CVE-2024-12088 and CVE-2024-12747.
`

func TestRsyncOK(t *testing.T) {
	const v327 = "rsync  version 3.2.7  protocol version 31"
	for _, c := range []struct {
		out, changelog string
		ok             bool
	}{
		{"rsync  version 3.4.1  protocol version 32\n", "", true},
		{"rsync  version v3.5.1-g315819c5  protocol", "", true},
		{v327, "", false},
		// Decision #39: a distribution build with the fixes backported.
		{v327, el9Changelog, true},
		{v327, debChangelog, true},
		// Only one of the two receiver fixes is not enough.
		{v327, "- Resolves: RHEL-70208 - Path traversal vulnerability in rsync\n", false},
		{v327, "  * Fix CVE-2024-12088.\n", false},
		// Older than --fsync (3.2.4), whatever the changelog says; the
		// floor itself and 3.3.x pass with the fixes (task rp4).
		{"rsync  version 3.2.4  protocol version 31", el9Changelog, true},
		{"rsync  version 3.2.3  protocol version 31", el9Changelog, false},
		{"rsync  version 3.3.0  protocol version 31", el9Changelog, true},
		{"rsync  version 3.1.3  protocol version 31", el9Changelog, false},
		{"rsync  version 2.6.9  protocol version 29", "", false},
		{"openrsync: protocol version 29", el9Changelog, false},
	} {
		if err := rsyncOK(c.out, c.changelog); (err == nil) != c.ok {
			t.Errorf("rsyncOK(%q, %d-byte changelog) = %v, want ok %t", c.out, len(c.changelog), err, c.ok)
		}
	}
}

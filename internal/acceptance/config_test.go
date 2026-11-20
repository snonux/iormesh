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

func TestRsyncOK(t *testing.T) {
	for out, ok := range map[string]bool{
		"rsync  version 3.4.1  protocol version 32\n": true,
		"rsync  version v3.5.1-g315819c5  protocol":   true,
		"rsync  version 3.2.7  protocol version 31":   false,
		"rsync  version 2.6.9  protocol version 29":   false,
		"openrsync: protocol version 29":              false,
	} {
		if err := rsyncOK(out); (err == nil) != ok {
			t.Errorf("rsyncOK(%q) = %v, want ok %t", out, err, ok)
		}
	}
}

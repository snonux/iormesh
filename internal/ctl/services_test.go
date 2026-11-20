package ctl

import (
	"bytes"
	"context"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"iormesh/internal/schema"
	"iormesh/internal/servicedb"
)

// servicesEnv points the default service database at a temp file holding
// one fused orders → inventory.svc:8443 connection.
func servicesEnv(t *testing.T) (env, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	e, stdout, stderr, _ := testEnv(t)
	path := filepath.Join(t.TempDir(), "iormesh", servicedb.FileName)
	e.servicedb = func() (string, error) { return path, nil }
	ctx := context.Background()
	d, err := servicedb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	orders := schema.AppNodeID("shop", "orders")
	o := servicedb.Observation{Host: "hA", ConnID: "a1", Role: servicedb.RoleClient,
		State: schema.StateFused, MergedID: "m1", Project: "shop",
		Client:   servicedb.End{Project: "shop", Host: "hA", ConnID: "a1", App: orders},
		Server:   servicedb.End{Project: "inv", Host: "hB", ConnID: "b1", Comm: "java"},
		Endpoint: netip.MustParseAddrPort("10.0.0.2:8443"), Hostname: "inventory.svc",
		TLS: 1, First: 1, Last: 2,
		Requests: []servicedb.Request{{Method: "GET", Scheme: "https", Host: "inventory.svc",
			Path: "/items\x1b[2J", Count: 3, First: 1, Last: 2}}}
	if err := d.Import(ctx, servicedb.Snapshot{Conns: []servicedb.Observation{o}}); err != nil {
		t.Fatal(err)
	}
	return e, stdout, stderr
}

// TestServicesNaming walks list → name → rules → list → edges → unname
// through the CLI.
func TestServicesNaming(t *testing.T) {
	e, stdout, stderr := servicesEnv(t)
	steps := []struct {
		args []string
		want []string // substrings of stdout
	}{
		{[]string{"services"}, []string{"-  ", "inventory.svc (10.0.0.2:8443)", "tls", "app:shop/orders"}},
		{[]string{"services", "name", "-port", "8443", "Inventory*", "inventory"},
			[]string{"rule 1: inventory* port 8443 -> inventory"}},
		{[]string{"services", "rules"}, []string{"1   inventory*", "8443", "inventory"}},
		{[]string{"services", "list", "-paths"}, []string{"inventory  inventory.svc", `GET "/items\x1b[2J"`}},
		{[]string{"services", "edges", "-from", "app:shop/orders"}, []string{"app:shop/orders  ->  inventory"}},
		{[]string{"services", "unname", "1"}, []string{"removed rule 1"}},
	}
	for _, s := range steps {
		stdout.Reset()
		if code := run(s.args, e); code != exitOK {
			t.Fatalf("%v: exit %d, stderr %s", s.args, code, stderr)
		}
		for _, w := range s.want {
			if !strings.Contains(stdout.String(), w) {
				t.Errorf("%v: stdout lacks %q:\n%s", s.args, w, stdout)
			}
		}
		if strings.Contains(stdout.String(), "\x1b") {
			t.Errorf("%v: raw escape character reached the terminal", s.args)
		}
	}
}

// TestServicesRejects (negative): bad input exits non-zero and leaves
// the rules as they were.
func TestServicesRejects(t *testing.T) {
	e, stdout, stderr := servicesEnv(t)
	for _, c := range []struct {
		args []string
		code int
	}{
		{[]string{"services", "name", "orders[", "orders"}, exitError},
		{[]string{"services", "name", "orders*", "my orders"}, exitError},
		{[]string{"services", "name", "-port", "70000", "orders*", "orders"}, exitUsage},
		{[]string{"services", "name", "orders*"}, exitUsage},
		{[]string{"services", "unname", "7"}, exitError},
		{[]string{"services", "unname", "x"}, exitUsage},
		{[]string{"services", "rename"}, exitUsage},
	} {
		if code := run(c.args, e); code != c.code {
			t.Errorf("%v: exit %d, want %d (stderr %s)", c.args, code, c.code, stderr)
		}
	}
	stdout.Reset()
	if code := run([]string{"services", "rules"}, e); code != exitOK {
		t.Fatalf("rules: exit %d", code)
	}
	if lines := strings.Count(stdout.String(), "\n"); lines != 1 {
		t.Errorf("rules after rejected edits:\n%s", stdout)
	}
}

package servicedb

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// TestAddRuleRejects (negative): an invalid rule is refused and nothing
// is stored.
func TestAddRuleRejects(t *testing.T) {
	ctx := context.Background()
	d, _ := openTemp(t)
	if _, err := d.AddRule(ctx, Rule{HostGlob: "orders*", Port: 8443, Service: "orders"}); err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]Rule{
		"bad glob":        {HostGlob: "orders[", Service: "orders"},
		"empty glob":      {HostGlob: "  ", Service: "orders"},
		"empty service":   {HostGlob: "x*"},
		"space":           {HostGlob: "x*", Service: "my orders"},
		"control":         {HostGlob: "x*", Service: "orders\x1b[2J"},
		"too long":        {HostGlob: "x*", Service: string(make([]byte, 65))},
		"duplicate":       {HostGlob: "orders*", Port: 8443, Service: "other"},
		"duplicate, case": {HostGlob: "ORDERS*", Port: 8443, Service: "other"},
	} {
		if _, err := d.AddRule(ctx, r); !errors.Is(err, ErrRule) {
			t.Errorf("%s: err %v, want ErrRule", name, err)
		}
	}
	rules, err := d.Rules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Service != "orders" {
		t.Errorf("rules %+v: the first rule must stand alone, unchanged", rules)
	}
	if err := d.RemoveRule(ctx, 42); !errors.Is(err, ErrNoRule) {
		t.Errorf("remove of a missing rule: %v, want ErrNoRule", err)
	}
}

// TestResolve: port-specific beats any-port, the longer glob beats the
// shorter, then the older rule; the IP matches when no name is known.
func TestResolve(t *testing.T) {
	rules := []Rule{
		{ID: 1, HostGlob: "*", Port: 8443, Service: "any-8443"},
		{ID: 2, HostGlob: "orders*", Service: "orders-any-port"},
		{ID: 3, HostGlob: "orders*", Port: 8443, Service: "orders"},
		{ID: 4, HostGlob: "orders.*", Port: 8443, Service: "orders-dot"},
		{ID: 5, HostGlob: "10.0.0.*", Port: 5432, Service: "db"},
		{ID: 6, HostGlob: "db*", Port: 5432, Service: "db-older"},
		{ID: 7, HostGlob: "db?", Port: 5432, Service: "db-newer"},
	}
	for _, c := range []struct {
		host, ip string
		port     uint16
		want     string
	}{
		{"orders.svc", "10.1.1.1", 8443, "orders-dot"},
		{"orders-2", "10.1.1.1", 8443, "orders"},
		{"ORDERS-2", "10.1.1.1", 8443, "orders"},
		{"orders-2", "10.1.1.1", 80, "orders-any-port"},
		{"billing", "10.1.1.1", 8443, "any-8443"},
		{"", "10.0.0.9", 5432, "db"},
		{"db1", "10.9.9.9", 5432, "db-older"},
		{"billing", "10.1.1.1", 80, ""},
		{"", "", 8443, ""},
	} {
		if got := resolve(rules, c.host, c.ip, c.port); got != c.want {
			t.Errorf("resolve(%q, %q, %d) = %q, want %q", c.host, c.ip, c.port, got, c.want)
		}
	}
}

// TestNamesAreRetroactive: a rule added after the import names the
// endpoints already stored, listings put the unnamed first, removing the
// rule unnames again — and no import touches a rule.
func TestNamesAreRetroactive(t *testing.T) {
	ctx := context.Background()
	d, _ := openTemp(t)
	s := observeMesh(t)
	importSnap(t, d, s)
	labels := func() []string { return endpointLabels(t, d) }
	want := []string{"10.0.0.2:8443", "127.0.0.1:9000", "203.0.113.9:5432", "inventory.svc:8443"}
	if got := labels(); !reflect.DeepEqual(got, want) {
		t.Fatalf("labels %v, want %v", got, want)
	}
	id, err := d.AddRule(ctx, Rule{HostGlob: "inventory*", Port: 8443, Service: "inventory"})
	if err != nil {
		t.Fatal(err)
	}
	importSnap(t, d, s)
	want = []string{"10.0.0.2:8443", "127.0.0.1:9000", "203.0.113.9:5432", "inventory"}
	if got := labels(); !reflect.DeepEqual(got, want) {
		t.Errorf("after the rule: %v, want %v", got, want)
	}
	ipRule, err := d.AddRule(ctx, Rule{HostGlob: "10.0.0.2", Service: "inventory"})
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"127.0.0.1:9000", "203.0.113.9:5432", "inventory", "inventory"}
	if got := labels(); !reflect.DeepEqual(got, want) {
		t.Errorf("unnamed first: %v, want %v", got, want)
	}
	if err := d.RemoveRule(ctx, id); err != nil {
		t.Fatal(err)
	}
	// The IP rule still names the endpoint whose hostname is known.
	if got := labels(); !reflect.DeepEqual(got, want) {
		t.Errorf("after removing the hostname rule: %v, want %v", got, want)
	}
	if err := d.RemoveRule(ctx, ipRule); err != nil {
		t.Fatal(err)
	}
	if got := labels(); got[3] != "inventory.svc:8443" {
		t.Errorf("after removing both: %v", got)
	}
}

// endpointLabels lists the endpoints' labels in listing order.
func endpointLabels(t *testing.T, d *DB) []string {
	t.Helper()
	eps, err := d.Endpoints(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range eps {
		out = append(out, e.Label())
	}
	return out
}

package cli

import (
	"context"
	"testing"
)

// TestHostSession is a smoke test on the build host: every field of the
// session header comes back non-empty, as Validate demands.
func TestHostSession(t *testing.T) {
	s, err := hostSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.BootID == "" || s.FQDN == "" || s.Kernel == "" || s.Arch == "" {
		t.Fatalf("incomplete session %+v", s)
	}
}

// TestCanonicalName: a dotted hostname is already the FQDN, and a short
// one the resolver cannot expand falls back to itself. The cancelled
// context keeps the test off the network: any lookup fails at once.
func TestCanonicalName(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, host := range []string{"db1.example.org", "nodot"} {
		if got := canonicalName(ctx, host); got != host {
			t.Errorf("canonicalName(%q) = %q, want it unchanged", host, got)
		}
	}
}

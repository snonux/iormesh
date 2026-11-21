package k8s

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// routeKubectl answers `get` with $FAKE_OUT, or fails as kubectl does
// for a resource the server does not serve when $FAKE_NOTYPE is set; it
// logs its argv one element per line.
const routeKubectl = `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a" >> "$FAKE_LOG"; done
if [ -n "$FAKE_NOTYPE" ]; then
  echo 'error: the server doesn'"'"'t have a resource type "ingressroutes"' >&2; exit 1
fi
cat "$FAKE_OUT"
`

func routeFake(t *testing.T) (log string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(routeKubectl), 0o700); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.json")
	if err := os.WriteFile(out, []byte(`{"items":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	log = filepath.Join(dir, "log")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_LOG", log)
	t.Setenv("FAKE_OUT", out)
	return log
}

// TestRouteReader: a get carries the context and namespace and reads
// the list as JSON, nothing else; a resource the server lacks is
// ErrNoResource; a resource outside the list, a bad context or a bad
// namespace never reaches kubectl.
func TestRouteReader(t *testing.T) {
	log := routeFake(t)
	r, err := NewRouteReader("kind-test", "prod")
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Get(context.Background(), "endpointslices.discovery.k8s.io")
	if err != nil || string(out) != `{"items":[]}` {
		t.Fatalf("get: %q, %v", out, err)
	}
	data, _ := os.ReadFile(log)
	want := "--context=kind-test\n--namespace=prod\nget\nendpointslices.discovery.k8s.io\n--output=json\n"
	if string(data) != want {
		t.Errorf("argv\n%s\nwant\n%s", data, want)
	}
	if _, err := r.Get(context.Background(), "secrets"); !errors.Is(err, ErrName) {
		t.Errorf("secrets: %v", err)
	}
	t.Setenv("FAKE_NOTYPE", "1")
	if _, err := r.Get(context.Background(), "ingressroutes.traefik.io"); !errors.Is(err, ErrNoResource) {
		t.Errorf("missing CRD: %v", err)
	}
	for _, bad := range [][2]string{{"-oProxy", "prod"}, {"kind-test", "Prod"}, {"kind-test", "a/b"}} {
		if _, err := NewRouteReader(bad[0], bad[1]); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	data, _ = os.ReadFile(log)
	if strings.Contains(string(data), "secrets") {
		t.Error("an unlisted resource reached kubectl")
	}
}

// TestCapBuffer: kubectl output past the limit is refused, not held.
func TestCapBuffer(t *testing.T) {
	c := &capBuffer{limit: 4}
	if _, err := c.Write([]byte("abcd")); err != nil || c.over {
		t.Fatalf("at the limit: %v", err)
	}
	if _, err := c.Write([]byte("e")); err == nil || !c.over || c.Len() != 4 {
		t.Errorf("past the limit: %v, over %v, len %d", err, c.over, c.Len())
	}
}

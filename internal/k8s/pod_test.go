package k8s

import (
	"strings"
	"testing"
)

func TestProjectForNamespace(t *testing.T) {
	for _, ns := range []string{"prod", "default", "kube-system", "a", "team-1", strings.Repeat("a", 63)} {
		if p, err := ProjectForNamespace(ns); err != nil || p != ns {
			t.Errorf("%q: %q, %v", ns, p, err)
		}
	}
	for _, ns := range []string{"", "Prod", "-prod", "prod-", "a.b", "a_b", "../etc", "a b",
		strings.Repeat("a", 64)} {
		if p, err := ProjectForNamespace(ns); err == nil {
			t.Errorf("%q: accepted as %q", ns, p)
		}
	}
}

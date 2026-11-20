package k8s

import (
	"strings"
	"testing"
)

func TestPodFromEnv(t *testing.T) {
	env := map[string]string{EnvPodName: "checkout-7d9f", EnvPodNamespace: "prod", EnvPodUID: uid}
	pod, ok := PodFromEnv(func(k string) string { return env[k] })
	if !ok || pod != (PodRef{"checkout-7d9f", "prod", uid}) {
		t.Fatalf("%+v, %v", pod, ok)
	}
	for _, k := range []string{EnvPodName, EnvPodNamespace, EnvPodUID} {
		partial := map[string]string{}
		for kk, v := range env {
			if kk != k {
				partial[kk] = v
			}
		}
		if _, ok := PodFromEnv(func(k string) string { return partial[k] }); ok {
			t.Errorf("without %s: a pod", k)
		}
	}
}

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

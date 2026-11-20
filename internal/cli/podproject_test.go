package cli

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"iormesh/internal/k8s"
)

// fakePodEnv makes podEnv return env's values, as the downward API would
// set them in a pod (internal/k8s/manifest.go).
func fakePodEnv(t *testing.T, env map[string]string) {
	t.Helper()
	orig := podEnv
	podEnv = func(k string) string { return env[k] }
	t.Cleanup(func() { podEnv = orig })
}

var inPod = map[string]string{"POD_NAME": "checkout-7d9f", "POD_NAMESPACE": "prod",
	"POD_UID": "3b1c9a52-7e0d-4c1f-9a8e-2f6d5b4c3a21"}

// In a pod, run's project defaults to the namespace (decision #21); an
// explicit -project still wins.
func TestRunProjectDefaultsToNamespace(t *testing.T) {
	fakeProcs(t, 10)
	fakePodEnv(t, inPod)
	cfg, err := parseRunFlags([]string{"-pid", "10", "-out", "o"}, &bytes.Buffer{})
	if err != nil || cfg.project != "prod" {
		t.Fatalf("project %q, %v; want the namespace", cfg.project, err)
	}
	cfg, err = parseRunFlags([]string{"-pid", "10", "-project", "shop", "-out", "o"}, &bytes.Buffer{})
	if err != nil || cfg.project != "shop" {
		t.Fatalf("project %q, %v; want the flag's", cfg.project, err)
	}
}

// In a pod, segments default to k8s.TraceRoot inside the emptyDir, not
// the server's /opt/iormesh/traces nor the 0777 volume root; -out alone
// still writes NDJSON only, and -trace-dir still wins. Outside a pod the
// server default stays.
func TestRunTraceDirInPod(t *testing.T) {
	fakeProcs(t, 10)
	fakePodEnv(t, inPod)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"-pid", "10"}, k8s.TraceRoot},
		{[]string{"-pid", "10", "-out", "o"}, ""},
		{[]string{"-pid", "10", "-trace-dir", "/srv/t"}, "/srv/t"},
	} {
		cfg, err := parseRunFlags(c.args, &bytes.Buffer{})
		if err != nil || cfg.traceDir != c.want {
			t.Errorf("%q: trace dir %q, %v; want %q", c.args, cfg.traceDir, err, c.want)
		}
	}
	if filepath.Dir(k8s.TraceRoot) != k8s.TracesMount {
		t.Errorf("TraceRoot %s is not one level inside the volume %s", k8s.TraceRoot, k8s.TracesMount)
	}
	fakePodEnv(t, map[string]string{})
	cfg, err := parseRunFlags([]string{"-pid", "10", "-project", "p"}, &bytes.Buffer{})
	if err != nil || cfg.traceDir != defaultTraceDir {
		t.Errorf("no pod: trace dir %q, %v; want %q", cfg.traceDir, err, defaultTraceDir)
	}
}

// -disk-max-bytes reaches the watchdog's byte cap; 0 (the default) is off.
func TestRunDiskMaxBytes(t *testing.T) {
	fakeProcs(t, 10)
	cfg, err := parseRunFlags([]string{"-pid", "10", "-project", "p", "-disk-max-bytes", "1536MiB"},
		&bytes.Buffer{})
	if err != nil || cfg.watchdog().MaxDirBytes != 1536<<20 {
		t.Fatalf("cap %+v, %v", cfg.watchdog(), err)
	}
	if cfg, err = parseRunFlags([]string{"-pid", "10", "-project", "p"}, &bytes.Buffer{}); err != nil ||
		cfg.watchdog().MaxDirBytes != 0 {
		t.Fatalf("default cap %+v, %v", cfg.watchdog(), err)
	}
	if _, err := parseRunFlags([]string{"-pid", "10", "-project", "p", "-disk-max-bytes", "2G"},
		&bytes.Buffer{}); !errors.Is(err, errUsage) {
		t.Fatalf("bad cap: %v", err)
	}
}

// Outside a pod -project stays required: one stray variable is not a
// pod, and a namespace that is no valid label is refused, not used.
func TestRunProjectNoPodDefault(t *testing.T) {
	fakeProcs(t, 10)
	cases := map[string]map[string]string{
		"no pod":         {},
		"namespace only": {"POD_NAMESPACE": "prod"},
		"no uid":         {"POD_NAME": "a", "POD_NAMESPACE": "prod"},
		"bad namespace":  {"POD_NAME": "a", "POD_NAMESPACE": "../etc", "POD_UID": "u"},
		"upper case":     {"POD_NAME": "a", "POD_NAMESPACE": "Prod", "POD_UID": "u"},
	}
	for name, env := range cases {
		fakePodEnv(t, env)
		if _, err := parseRunFlags([]string{"-pid", "10", "-out", "o"}, &bytes.Buffer{}); !errors.Is(err, errUsage) {
			t.Errorf("%s: err %v, want a usage error", name, err)
		}
	}
}

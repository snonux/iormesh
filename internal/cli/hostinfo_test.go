package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"iormesh/internal/k8s"
)

// envOf is a getenv over env.
func envOf(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

// fakeMachineID points machineIDPath at a file holding id, or at a path
// that does not exist when id is "".
func fakeMachineID(t *testing.T, id string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "machine-id")
	if id != "" {
		if err := os.WriteFile(p, []byte(id+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	orig := machineIDPath
	machineIDPath = p
	t.Cleanup(func() { machineIDPath = orig })
}

// TestDescribeHostOutsidePod: no pod variable set, the host id is the
// machine-id and every field of the session header comes back non-empty,
// as Validate demands. The resolver may be asked (the build host's name).
func TestDescribeHostOutsidePod(t *testing.T) {
	fakeMachineID(t, "0123456789abcdef0123456789abcdef")
	id, s, err := describeHost(context.Background(), envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	if id != "0123456789abcdef0123456789abcdef" {
		t.Errorf("host id %q, want the machine-id", id)
	}
	if s.BootID == "" || s.FQDN == "" || s.Kernel == "" || s.Arch == "" {
		t.Fatalf("incomplete session %+v", s)
	}
	fakeMachineID(t, "")
	if _, _, err := describeHost(context.Background(), envOf(nil)); err == nil {
		t.Error("no machine-id outside a pod: described anyway")
	}
}

// TestDescribeHostInPod: in a pod the host is the pod — host id
// "pod-<uid>", FQDN <name>.<namespace>.<uid[:8]> — with no machine-id
// in the image (the scratch tracer image has none), and a machine-id the
// image does carry is ignored.
func TestDescribeHostInPod(t *testing.T) {
	for _, mid := range []string{"", "feedfacefeedfacefeedfacefeedface"} {
		fakeMachineID(t, mid)
		id, s, err := describeHost(context.Background(), envOf(inPod))
		if err != nil {
			t.Fatalf("machine-id %q: %v", mid, err)
		}
		if id != "pod-3b1c9a52-7e0d-4c1f-9a8e-2f6d5b4c3a21" {
			t.Errorf("machine-id %q: host id %q", mid, id)
		}
		if s.FQDN != "checkout-7d9f.prod.3b1c9a52" {
			t.Errorf("machine-id %q: FQDN %q", mid, s.FQDN)
		}
		if s.BootID == "" || s.Kernel == "" || s.Arch == "" {
			t.Errorf("incomplete session %+v", s)
		}
	}
}

// TestDescribeHostRefusesPodEnv: a partial or malformed pod environment
// stops the run, even with a machine-id to fall back to.
func TestDescribeHostRefusesPodEnv(t *testing.T) {
	fakeMachineID(t, "0123456789abcdef0123456789abcdef")
	for name, env := range map[string]map[string]string{
		"uid only":      {"POD_UID": inPod["POD_UID"]},
		"no namespace":  {"POD_NAME": "a", "POD_UID": inPod["POD_UID"]},
		"forged uid":    {"POD_NAME": "a", "POD_NAMESPACE": "prod", "POD_UID": "../../etc"},
		"bad namespace": {"POD_NAME": "a", "POD_NAMESPACE": "Prod", "POD_UID": inPod["POD_UID"]},
		"bad name":      {"POD_NAME": "a/b", "POD_NAMESPACE": "prod", "POD_UID": inPod["POD_UID"]},
	} {
		if id, _, err := describeHost(context.Background(), envOf(env)); !errors.Is(err, k8s.ErrPodEnv) {
			t.Errorf("%s: host id %q, err %v; want ErrPodEnv", name, id, err)
		}
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

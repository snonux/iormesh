// Package k8s holds the scripts that set up the clusters M7's acceptance
// runs against (docs/testing.md, "M7 on an existing cluster"). These tests
// run el9-k3s.sh's `down` against recording fakes, never a real host: PATH
// holds only the fakes, and $IORMESH_K3S_STATE and $IORMESH_K3S_BIN point
// the script's state file and k3s's own scripts into a temporary directory
// (task ni4).
package k8s

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakes are the commands `down` and `status` may run. Each fake appends
// its name and arguments to the log and succeeds, so systemctl reports
// firewalld active and rpm reports every package installed.
var fakes = []string{"k3s-killall.sh", "k3s-uninstall.sh", "rm", "dnf",
	"firewall-cmd", "systemctl", "curl", "rpm", "getenforce", "df", "sha256sum"}

// runDown runs `el9-k3s.sh down` with the fakes on PATH and, unless state
// is empty, a state file holding state. It returns the fakes' log, the
// script's stderr and its error.
func runDown(t *testing.T, state string) (calls, stderr string, err error) {
	t.Helper()
	bash, lookErr := exec.LookPath("bash")
	if lookErr != nil {
		t.Skip("no bash")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "calls")
	for _, name := range fakes {
		fake := "#!/bin/sh\necho \"" + name + " $*\" >> '" + log + "'\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(fake), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stateFile := filepath.Join(dir, "state")
	if state != "" {
		if err := os.WriteFile(stateFile, []byte(state), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(bash, "el9-k3s.sh", "down")
	cmd.Env = []string{"PATH=" + bin, "IORMESH_K3S_STATE=" + stateFile, "IORMESH_K3S_BIN=" + bin}
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	err = cmd.Run()
	b, readErr := os.ReadFile(log)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	return string(b), errBuf.String(), err
}

// TestDownRefusesWithoutState: without the state file `up` writes, `down`
// exits non-zero before it runs anything - no uninstall, no rm -rf of
// /etc/rancher and /var/lib/rancher, no dnf, no firewalld change.
func TestDownRefusesWithoutState(t *testing.T) {
	calls, stderr, err := runDown(t, "")
	if err == nil {
		t.Error("down without a state file succeeded, want an error")
	}
	if calls != "" {
		t.Errorf("down without a state file ran:\n%s", calls)
	}
	if !strings.Contains(stderr, "up did not install k3s here") {
		t.Errorf("stderr = %q, want the refusal", stderr)
	}
}

// TestDownFirewalld: `down` removes from firewalld's trusted zone only the
// sources `up` recorded as added, none when both pre-existed.
func TestDownFirewalld(t *testing.T) {
	const remove42 = "--remove-source=10.42.0.0/16"
	const remove43 = "--remove-source=10.43.0.0/16"
	tests := []struct {
		name, state string
		want, never []string
	}{
		{"both pre-existed",
			"container_selinux_before=yes\nrepo_before=yes\nfirewalld_added=\n",
			nil, []string{remove42, remove43}},
		{"no record (up failed before firewalld)",
			"container_selinux_before=yes\nrepo_before=yes\n",
			nil, []string{remove42, remove43}},
		{"one added",
			"container_selinux_before=yes\nrepo_before=yes\nfirewalld_added=10.43.0.0/16\n",
			[]string{remove43}, []string{remove42}},
		{"one added, then the second",
			"container_selinux_before=yes\nrepo_before=yes\nfirewalld_added=\n" +
				"firewalld_added=10.42.0.0/16\nfirewalld_added=10.42.0.0/16 10.43.0.0/16\n",
			[]string{remove42, remove43}, nil},
		{"both added",
			"container_selinux_before=yes\nrepo_before=yes\nfirewalld_added=10.42.0.0/16 10.43.0.0/16\n",
			[]string{remove42, remove43}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls, stderr, err := runDown(t, tt.state)
			if err != nil {
				t.Fatalf("down: %v\n%s", err, stderr)
			}
			if !strings.Contains(calls, "k3s-uninstall.sh") {
				t.Errorf("down did not uninstall k3s:\n%s", calls)
			}
			for _, w := range tt.want {
				if !strings.Contains(calls, w) {
					t.Errorf("down did not run firewall-cmd %s:\n%s", w, calls)
				}
			}
			for _, n := range tt.never {
				if strings.Contains(calls, n) {
					t.Errorf("down ran firewall-cmd %s:\n%s", n, calls)
				}
			}
		})
	}
}

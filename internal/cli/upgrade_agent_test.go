package cli

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aead.dev/minisign"

	"iormesh/internal/release"
)

// agentMeta is the agent jar signed at the rig's running version (1.2.0).
var agentMeta = release.Meta{File: release.AgentFile, Version: "1.2.0"}

// installedAgentBytes is the rig's installed jar, nil when there is none.
func (r upgradeRig) installedAgentBytes(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(r.u.agentPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestUpgradeInstallsAgentJar (task kf4): a jar signed as the agent at
// the tracer's version lands at lib/iormesh-agent.jar, root's (here the
// rig's owner) and 0644 in a 0755 lib/ the upgrade created, the tracer
// untouched; the same bytes again are a no-op.
func TestUpgradeInstallsAgentJar(t *testing.T) {
	r := newUpgradeRig(t)
	staged := r.stage(t, []byte("agent jar"), agentMeta)
	if err := r.u.upgrade(staged, false); err != nil {
		t.Fatal(err)
	}
	if got := r.installedAgentBytes(t); string(got) != "agent jar" {
		t.Fatalf("installed jar %q", got)
	}
	if got := r.installed(t); got != oldBinary {
		t.Fatalf("tracer changed to %q", got)
	}
	for path, mode := range map[string]os.FileMode{r.u.agentPath(): 0o644,
		filepath.Dir(r.u.agentPath()): os.ModeDir | 0o755} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode() != mode {
			t.Errorf("%s: mode %v (%v), want %v", path, info.Mode(), err, mode)
		}
	}
	if !strings.Contains(r.out.String(), "installed iormesh-agent.jar 1.2.0 as") {
		t.Errorf("output %q", r.out.String())
	}
	r.out.Reset()
	r.throughRule()
	if err := r.u.upgrade(r.stage(t, []byte("agent jar"), agentMeta), false); err != nil {
		t.Fatalf("same jar again through the rule: %v", err)
	}
	if !strings.Contains(r.out.String(), "already installed") {
		t.Errorf("rerun output %q", r.out.String())
	}
}

// agentRefusal is one negative jar upgrade: setup breaks one thing in a
// fresh rig (with a jar "old jar" installed when preinstall) and returns
// the staged path and -allow-downgrade.
type agentRefusal struct {
	name       string
	preinstall bool
	setup      func(t *testing.T, r *upgradeRig) (string, bool)
	want       string
}

// runAgentRefusals requires each case's error, the tracer unchanged and
// the installed jar (or its absence) exactly as before.
func runAgentRefusals(t *testing.T, tests []agentRefusal) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newUpgradeRig(t)
			if tt.preinstall {
				if err := r.u.upgrade(r.stage(t, []byte("old jar"), agentMeta), false); err != nil {
					t.Fatal(err)
				}
			}
			staged, allow := tt.setup(t, &r)
			before := r.installedAgentBytes(t)
			err := r.u.upgrade(staged, allow)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("upgrade err = %v, want %q", err, tt.want)
			}
			if after := r.installedAgentBytes(t); !bytes.Equal(before, after) {
				t.Fatalf("installed jar changed from %q to %q", before, after)
			}
			if got := r.installed(t); got != oldBinary {
				t.Fatalf("tracer changed to %q", got)
			}
		})
	}
}

// TestUpgradeRefusesAgentJar: a tampered or foreign-signed jar, one whose
// version is not the tracer's, a rebuild through the rule and a jar
// slipped in as the tracer's name are all refused before lib/ is written.
func TestUpgradeRefusesAgentJar(t *testing.T) {
	runAgentRefusals(t, []agentRefusal{
		{"tampered jar", false, func(t *testing.T, r *upgradeRig) (string, bool) {
			p := r.stage(t, []byte("agent jar"), agentMeta)
			mustWrite(t, p, []byte("evil jar"), 0o644)
			return p, false
		}, "does not match its signature"},
		{"tampered jar over an installed one", true, func(t *testing.T, r *upgradeRig) (string, bool) {
			p := r.stage(t, []byte("new jar"), agentMeta)
			mustWrite(t, p, []byte("evil jar"), 0o644)
			return p, false
		}, "does not match its signature"},
		{"wrong key", false, func(t *testing.T, r *upgradeRig) (string, bool) {
			_, other, err := minisign.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			r.priv = other
			return r.stage(t, []byte("agent jar"), agentMeta), false
		}, "the pinned key is"},
		{"newer than the tracer", false, func(t *testing.T, r *upgradeRig) (string, bool) {
			return r.stage(t, []byte("agent jar"), release.Meta{File: release.AgentFile, Version: "1.3.0"}), false
		}, "does not match the installed tracer 1.2.0"},
		{"older than the tracer", true, func(t *testing.T, r *upgradeRig) (string, bool) {
			return r.stage(t, []byte("agent jar"), release.Meta{File: release.AgentFile, Version: "1.1.0"}), false
		}, "does not match the installed tracer 1.2.0"},
		{"allow-downgrade", false, func(t *testing.T, r *upgradeRig) (string, bool) {
			return r.stage(t, []byte("agent jar"), agentMeta), true
		}, "applies to the tracer only"},
		{"rebuild through the rule", true, func(t *testing.T, r *upgradeRig) (string, bool) {
			r.throughRule()
			return r.stage(t, []byte("rebuilt jar"), agentMeta), false
		}, "installed with other bytes"},
		{"jar under another name", false, func(t *testing.T, r *upgradeRig) (string, bool) {
			return r.stage(t, []byte("agent jar"), release.Meta{File: "agent.jar", Version: "1.2.0"}), false
		}, "signed as agent.jar"},
	})
}

// TestUpgradeRefusesAgentLayout: a lib/ or installed jar that someone
// else could change is refused, never repaired.
func TestUpgradeRefusesAgentLayout(t *testing.T) {
	runAgentRefusals(t, []agentRefusal{
		{"symlinked lib", false, func(t *testing.T, r *upgradeRig) (string, bool) {
			if err := os.Symlink(r.staging, filepath.Dir(r.u.agentPath())); err != nil {
				t.Fatal(err)
			}
			return r.stage(t, []byte("agent jar"), agentMeta), false
		}, "not a directory"},
		{"group-writable lib", true, func(t *testing.T, r *upgradeRig) (string, bool) {
			if err := os.Chmod(filepath.Dir(r.u.agentPath()), 0o775); err != nil {
				t.Fatal(err)
			}
			return r.stage(t, []byte("new jar"), agentMeta), false
		}, "group or other may write"},
		{"private jar", true, func(t *testing.T, r *upgradeRig) (string, bool) {
			if err := os.Chmod(r.u.agentPath(), 0o600); err != nil {
				t.Fatal(err)
			}
			return r.stage(t, []byte("new jar"), agentMeta), false
		}, "want 0644"},
		{"symlinked jar", false, func(t *testing.T, r *upgradeRig) (string, bool) {
			mustMkdir(t, filepath.Dir(r.u.agentPath()))
			if err := os.Symlink(filepath.Join(r.staging, "x"), r.u.agentPath()); err != nil {
				t.Fatal(err)
			}
			return r.stage(t, []byte("agent jar"), agentMeta), false
		}, "is a symlink"},
	})
}

// TestUpgradeAgentRootReinstall: root itself, not through the rule, may
// replace the jar with other bytes of the same version.
func TestUpgradeAgentRootReinstall(t *testing.T) {
	r := newUpgradeRig(t)
	for _, data := range []string{"old jar", "rebuilt jar"} {
		if err := r.u.upgrade(r.stage(t, []byte(data), agentMeta), false); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.installedAgentBytes(t); string(got) != "rebuilt jar" {
		t.Fatalf("installed jar %q", got)
	}
}

// TestUpgradeTracerDropsAgent (task kf4's review, N1): a tracer upgrade
// to another version removes the jar checked against the old one, so the
// new tracer never loads a jar of another release; a same-version
// reinstall (root) and a no-op keep it.
func TestUpgradeTracerDropsAgent(t *testing.T) {
	r := newUpgradeRig(t)
	if err := r.u.upgrade(r.stage(t, []byte("agent jar"), agentMeta), false); err != nil {
		t.Fatal(err)
	}
	same := release.Meta{File: "iormesh-linux-amd64", Version: "1.2.0"}
	if err := r.u.upgrade(r.stage(t, []byte("rebuilt tracer"), same), false); err != nil {
		t.Fatal(err)
	}
	if r.installedAgentBytes(t) == nil {
		t.Fatal("a same-version tracer reinstall removed the jar")
	}
	if err := r.u.upgrade(r.stage(t, []byte("new tracer"), goodMeta), false); err != nil {
		t.Fatal(err)
	}
	if got := r.installedAgentBytes(t); got != nil {
		t.Fatalf("jar %q kept after the tracer moved to 1.3.0", got)
	}
	if !strings.Contains(r.out.String(), "removed "+r.u.agentPath()) {
		t.Errorf("output %q", r.out.String())
	}
}

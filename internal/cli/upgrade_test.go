package cli

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aead.dev/minisign"

	"iormesh/internal/release"
)

// upgradeRig is a temporary install layout the test's own uid owns, with
// a throwaway release key generated per run (no key is committed) pinned
// in it, and the old binary installed.
type upgradeRig struct {
	u       upgrader
	priv    minisign.PrivateKey
	staging string
	out     *bytes.Buffer
}

const oldBinary = "old tracer"

func newUpgradeRig(t *testing.T) upgradeRig {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	root := filepath.Join(base, "opt", "iormesh")
	for _, d := range []string{"bin", "etc"} {
		mustMkdir(t, filepath.Join(root, d))
	}
	keyText, _ := pub.MarshalText()
	mustWrite(t, filepath.Join(root, pinnedKeyRel), keyText, 0o644)
	mustWrite(t, filepath.Join(root, binRel), []byte(oldBinary), 0o755)
	staging := filepath.Join(base, "staging")
	mustMkdir(t, staging)
	out := &bytes.Buffer{}
	// self is the installed binary as it was when "this process" started:
	// a later rename over it gives the path a new inode, self keeps the old.
	self := mustStat(t, filepath.Join(root, binRel))
	u := upgrader{root: root, owner: os.Getuid(), group: os.Getgid(), sudoUID: -1, running: "1.2.0",
		goarch: "amd64", stdout: out, self: func() (os.FileInfo, error) { return self, nil }}
	return upgradeRig{u: u, priv: priv, staging: staging, out: out}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

// stage writes data and its signature as m into the staging dir.
func (r upgradeRig) stage(t *testing.T, data []byte, m release.Meta) string {
	t.Helper()
	sig, err := release.Sign(r.priv, data, m, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.staging, "iormesh.staged")
	mustWrite(t, path, data, 0o644)
	mustWrite(t, path+release.SigSuffix, sig, 0o644)
	return path
}

func (r upgradeRig) installed(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(r.u.bin())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestUpgradeInstallsVerifiedBuild(t *testing.T) {
	r := newUpgradeRig(t)
	staged := r.stage(t, []byte("new tracer"), release.Meta{File: "iormesh-linux-amd64", Version: "1.3.0"})
	if err := r.u.upgrade(staged, false); err != nil {
		t.Fatal(err)
	}
	if got := r.installed(t); got != "new tracer" {
		t.Fatalf("installed %q", got)
	}
	info, err := os.Lstat(r.u.bin())
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o755 {
		t.Fatalf("installed mode %v, %v", info.Mode(), err)
	}
	if !strings.Contains(r.out.String(), "from 1.2.0 to 1.3.0") {
		t.Errorf("output %q", r.out.String())
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(r.u.bin()), ".iormesh-upgrade-*"))
	if len(leftovers) != 0 {
		t.Errorf("temp files left: %v", leftovers)
	}
}

// refusal is one negative upgrade case: setup breaks one thing in a fresh
// rig and returns the staged path and -allow-downgrade.
type refusal struct {
	name  string
	setup func(t *testing.T, r *upgradeRig) (staged string, allowDowngrade bool)
	want  string
}

var goodMeta = release.Meta{File: "iormesh-linux-amd64", Version: "1.3.0"}

// runRefusals runs each case on a fresh rig and requires its error and
// an installed binary left exactly as setup left it.
func runRefusals(t *testing.T, tests []refusal) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newUpgradeRig(t)
			staged, allow := tt.setup(t, &r)
			before, _ := os.ReadFile(r.u.bin())
			err := r.u.upgrade(staged, allow)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("upgrade err = %v, want %q", err, tt.want)
			}
			if after, _ := os.ReadFile(r.u.bin()); !bytes.Equal(before, after) {
				t.Fatalf("installed binary changed from %q to %q", before, after)
			}
		})
	}
}

// TestUpgradeRefusesSignature: the staged build or its signature is not
// what the pinned key vouches for this host.
func TestUpgradeRefusesSignature(t *testing.T) {
	runRefusals(t, []refusal{
		{"tampered binary", func(t *testing.T, r *upgradeRig) (string, bool) {
			p := r.stage(t, []byte("new tracer"), goodMeta)
			mustWrite(t, p, []byte("evil tracer"), 0o644)
			return p, false
		}, "does not match its signature"},
		{"bad signature", func(t *testing.T, r *upgradeRig) (string, bool) {
			p := r.stage(t, []byte("new tracer"), goodMeta)
			mustWrite(t, p+release.SigSuffix, []byte("untrusted comment: x\nnot base64\n"), 0o644)
			return p, false
		}, "signature verification failed"},
		{"missing signature", func(t *testing.T, r *upgradeRig) (string, bool) {
			p := r.stage(t, []byte("new tracer"), goodMeta)
			_ = os.Remove(p + release.SigSuffix)
			return p, false
		}, "no such file"},
		{"wrong key", func(t *testing.T, r *upgradeRig) (string, bool) {
			_, other, err := minisign.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			r.priv = other
			return r.stage(t, []byte("new tracer"), goodMeta), false
		}, "the pinned key is"},
		{"other arch", func(t *testing.T, r *upgradeRig) (string, bool) {
			return r.stage(t, []byte("arm"), release.Meta{File: "iormesh-linux-arm64", Version: "1.3.0"}), false
		}, "signed as iormesh-linux-arm64"},
		{"controller binary", func(t *testing.T, r *upgradeRig) (string, bool) {
			return r.stage(t, []byte("ctl"), release.Meta{File: "iormeshctl-linux-amd64", Version: "1.3.0"}), false
		}, "signed as iormeshctl-linux-amd64"},
		{"symlinked staged file", func(t *testing.T, r *upgradeRig) (string, bool) {
			p := r.stage(t, []byte("new tracer"), goodMeta)
			link := filepath.Join(r.staging, "link")
			if err := os.Symlink(p, link); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(p+release.SigSuffix, link+release.SigSuffix); err != nil {
				t.Fatal(err)
			}
			return link, false
		}, "is a symlink"},
	})
}

// TestUpgradeRefusesLayout: the install is not the root-owned layout
// docs/security.md requires, so it is refused, never repaired.
func TestUpgradeRefusesLayout(t *testing.T) {
	runRefusals(t, []refusal{
		{"symlinked install", func(t *testing.T, r *upgradeRig) (string, bool) {
			target := filepath.Join(r.staging, "elsewhere")
			mustWrite(t, target, []byte(oldBinary), 0o755)
			_ = os.Remove(r.u.bin())
			if err := os.Symlink(target, r.u.bin()); err != nil {
				t.Fatal(err)
			}
			return r.stage(t, []byte("new tracer"), goodMeta), false
		}, "is not a regular file"},
		{"writable bin dir", func(t *testing.T, r *upgradeRig) (string, bool) {
			if err := os.Chmod(filepath.Dir(r.u.bin()), 0o777); err != nil {
				t.Fatal(err)
			}
			return r.stage(t, []byte("new tracer"), goodMeta), false
		}, "group or other may write"},
		{"foreign install", func(t *testing.T, r *upgradeRig) (string, bool) {
			r.u.owner = os.Getuid() + 1
			return r.stage(t, []byte("new tracer"), goodMeta), false
		}, "is owned by uid"},
		{"group-writable key", func(t *testing.T, r *upgradeRig) (string, bool) {
			if err := os.Chmod(filepath.Join(r.u.root, pinnedKeyRel), 0o666); err != nil {
				t.Fatal(err)
			}
			return r.stage(t, []byte("new tracer"), goodMeta), false
		}, "group or other may write"},
		{"no pinned key", func(t *testing.T, r *upgradeRig) (string, bool) {
			_ = os.Remove(filepath.Join(r.u.root, pinnedKeyRel))
			return r.stage(t, []byte("new tracer"), goodMeta), false
		}, "no such file"},
	})
}

// throughRule makes the rig's upgrade one that came through the sudoers
// rule, called by the test's own uid.
func (r *upgradeRig) throughRule() {
	r.u.gated, r.u.sudoUID = true, os.Getuid()
}

// TestUpgradeRefusesVersion: downgrades, and through the rule a same
// version with other bytes or an install that changed under this process.
func TestUpgradeRefusesVersion(t *testing.T) {
	runRefusals(t, []refusal{
		{"downgrade", func(t *testing.T, r *upgradeRig) (string, bool) {
			return r.stage(t, []byte("old build"), release.Meta{File: goodMeta.File, Version: "1.1.9"}), false
		}, "refusing to downgrade from 1.2.0 to 1.1.9"},
		{"downgrade forced through the rule", func(t *testing.T, r *upgradeRig) (string, bool) {
			r.throughRule()
			return r.stage(t, []byte("old build"), release.Meta{File: goodMeta.File, Version: "1.1.9"}), true
		}, "-allow-downgrade is not allowed through the sudoers rule"},
		{"same version, other bytes, through the rule", func(t *testing.T, r *upgradeRig) (string, bool) {
			r.throughRule()
			return r.stage(t, []byte("rebuild"), release.Meta{File: goodMeta.File, Version: "1.2.0"}), false
		}, "must carry a new version"},
		// The ^Z race: a 1.2.0 process started an upgrade to a vulnerable
		// 1.2.1; meanwhile root installed the fixed 1.3.0 (a new inode).
		// Compared with its own 1.2.0, 1.2.1 looks like an upgrade.
		{"installed binary replaced since start", func(t *testing.T, r *upgradeRig) (string, bool) {
			r.throughRule()
			fixed := filepath.Join(filepath.Dir(r.u.bin()), "fixed")
			mustWrite(t, fixed, []byte("fixed 1.3.0"), 0o755)
			if err := os.Rename(fixed, r.u.bin()); err != nil {
				t.Fatal(err)
			}
			return r.stage(t, []byte("vulnerable"), release.Meta{File: goodMeta.File, Version: "1.2.1"}), false
		}, "changed since this upgrade started"},
	})
}

// TestUpgradeThroughRule: the rule's happy path — the installed binary is
// this process's, the staged build newer.
func TestUpgradeThroughRule(t *testing.T) {
	r := newUpgradeRig(t)
	r.throughRule()
	if err := r.u.upgrade(r.stage(t, []byte("new tracer"), goodMeta), false); err != nil {
		t.Fatal(err)
	}
	if got := r.installed(t); got != "new tracer" {
		t.Fatalf("installed %q", got)
	}
}

// TestUpgradeWaitsForLock: an upgrade holding bin/'s lock makes a second
// one wait until it is released, so their checks and renames never
// interleave.
func TestUpgradeWaitsForLock(t *testing.T) {
	r := newUpgradeRig(t)
	staged := r.stage(t, []byte("new tracer"), goodMeta)
	unlock, err := r.u.lock()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- r.u.upgrade(staged, false) }()
	select {
	case err := <-done:
		t.Fatalf("upgrade finished while the lock was held: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := r.installed(t); got != "new tracer" {
		t.Fatalf("installed %q", got)
	}
}

// TestUpgradeStagedOwner: through the rule root reads a staged file only
// if the sudo caller owns it, so the rule cannot probe others' files;
// root directly may stage its own. The rig's uid plays root (owner).
func TestUpgradeStagedOwner(t *testing.T) {
	r := newUpgradeRig(t)
	staged := r.stage(t, []byte("new tracer"), release.Meta{File: "iormesh-linux-amd64", Version: "1.3.0"})
	me := os.Getuid()
	tests := []struct {
		name    string
		sudoUID int
		gated   bool
		ok      bool
	}{
		{"root directly, own file", -1, false, true},
		{"rule, caller's file", me, true, true},
		{"rule, root's file", me + 1, true, false},
		{"operator's own sudo, root's file", me + 1, false, true},
	}
	for _, tt := range tests {
		r.u.sudoUID, r.u.gated = tt.sudoUID, tt.gated
		err := r.u.stagedCheck(staged, mustStat(t, staged))
		if tt.ok != (err == nil) || (err != nil && !errors.Is(err, release.ErrUnsafeFile)) {
			t.Errorf("%s: %v", tt.name, err)
		}
	}
	r.u.owner = me + 1
	r.u.sudoUID, r.u.gated = me+2, false
	if err := r.u.stagedCheck(staged, mustStat(t, staged)); err == nil {
		t.Error("a file neither root nor the caller owns was admitted")
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// TestUpgradeDowngradeAndReinstall: root directly may downgrade with the
// flag; the same version's identical bytes are a no-op, and a rebuild of
// the same version is installed (root only: the rule refuses it).
func TestUpgradeDowngradeAndReinstall(t *testing.T) {
	r := newUpgradeRig(t)
	staged := r.stage(t, []byte("old build"), release.Meta{File: "iormesh-linux-amd64", Version: "1.1.9"})
	if err := r.u.upgrade(staged, true); err != nil || r.installed(t) != "old build" {
		t.Fatalf("forced downgrade: %v, installed %q", err, r.installed(t))
	}
	same := release.Meta{File: "iormesh-linux-amd64", Version: "1.2.0"}
	staged = r.stage(t, []byte(oldBinary), same)
	mustWrite(t, r.u.bin(), []byte(oldBinary), 0o755)
	r.out.Reset()
	if err := r.u.upgrade(staged, false); err != nil || !strings.Contains(r.out.String(), "already installed") {
		t.Fatalf("same build: %v, %q", err, r.out.String())
	}
	staged = r.stage(t, []byte("rebuild"), same)
	if err := r.u.upgrade(staged, false); err != nil || r.installed(t) != "rebuild" {
		t.Fatalf("same-version rebuild: %v, installed %q", err, r.installed(t))
	}
}

// TestUpgradeCommandArgs: argument errors are usage errors, and a non-root
// caller is refused before anything is read.
func TestUpgradeCommandArgs(t *testing.T) {
	for _, args := range [][]string{nil, {"a", "b"}, {"-key", "k", "a"}} {
		if err := upgradeCommand(args, &bytes.Buffer{}, &bytes.Buffer{}); !errors.Is(err, errUsage) {
			t.Errorf("upgrade %v: %v, want usage error", args, err)
		}
	}
	if os.Geteuid() != 0 {
		err := upgradeCommand([]string{"/nonexistent"}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "run it through sudo") {
			t.Errorf("non-root upgrade: %v", err)
		}
	}
}

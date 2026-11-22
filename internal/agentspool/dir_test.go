package agentspool

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// testDir is a Dir over the directory of a test ring (4242.ring) whose
// process 4242 belongs to uid, or is gone when alive is false.
func testDir(t *testing.T, uid uint32, alive bool) (*Dir, string) {
	t.Helper()
	path, _ := newTestRing(t, testCap)
	d := NewDir(filepath.Dir(path))
	d.owner = func(uint64) (uint32, bool) { return uid, alive }
	return d, filepath.Dir(path)
}

func scanOnce(t *testing.T, d *Dir) ([]*Ring, []error) {
	t.Helper()
	rings, skipped, err := d.Scan()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rings {
		t.Cleanup(func() { _ = r.Close() })
	}
	return rings, skipped
}

// TestDirScan: a ring is opened once; a ring still being set up waits
// for the next scan; other files are ignored.
func TestDirScan(t *testing.T) {
	d, dir := testDir(t, uint32(os.Getuid()), true)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "4243.ring"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rings, skipped := scanOnce(t, d)
	if len(rings) != 1 || len(skipped) != 0 || rings[0].Header().PID != 4242 {
		t.Fatalf("rings %d, skipped %v", len(rings), skipped)
	}
	if rings, skipped := scanOnce(t, d); len(rings)+len(skipped) != 0 {
		t.Errorf("second scan: rings %d, skipped %v", len(rings), skipped)
	}
	for k := range d.done {
		if strings.HasPrefix(k, "4243.ring") {
			t.Error("a ring not set up yet is given up")
		}
	}
	// A reused pid: the agent deletes the stale file and creates a new
	// one, a new inode under the same name, which is opened again.
	path := filepath.Join(dir, "4242.ring")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if rings, _ := scanOnce(t, d); len(rings) != 1 {
		t.Errorf("new ring under a known name: %d rings", len(rings))
	}
}

// TestDirRejects: a ring whose owner cannot be shown to be its JVM's is
// skipped for good, never opened.
func TestDirRejects(t *testing.T) {
	cases := map[string]struct {
		uid   uint32
		alive bool
		edit  func(t *testing.T, dir string)
	}{
		"other owner": {uid: uint32(os.Getuid()) + 1, alive: true},
		// The owner is checked before the header is even mapped: a
		// foreign file is ErrNotRing, never parsed (here: ErrCorrupt).
		"other owner, bad header": {uid: uint32(os.Getuid()) + 1, alive: true,
			edit: func(t *testing.T, dir string) {
				corruptMagic(t, filepath.Join(dir, "4242.ring"))
			}},
		// A ring whose process is gone is removed, not reported
		// (TestDirRemovesDeadRings).
		"pid mismatch": {uid: uint32(os.Getuid()), alive: true, edit: func(t *testing.T, dir string) {
			mustRename(t, filepath.Join(dir, "4242.ring"), filepath.Join(dir, "4241.ring"))
		}},
		"symlink": {uid: uint32(os.Getuid()), alive: true, edit: func(t *testing.T, dir string) {
			mustRename(t, filepath.Join(dir, "4242.ring"), filepath.Join(dir, "real"))
			if err := os.Symlink("real", filepath.Join(dir, "4242.ring")); err != nil {
				t.Fatal(err)
			}
		}},
		"hard link": {uid: uint32(os.Getuid()), alive: true, edit: func(t *testing.T, dir string) {
			if err := os.Link(filepath.Join(dir, "4242.ring"), filepath.Join(dir, "x")); err != nil {
				t.Fatal(err)
			}
		}},
		"bad name": {uid: uint32(os.Getuid()), alive: true, edit: func(t *testing.T, dir string) {
			mustRename(t, filepath.Join(dir, "4242.ring"), filepath.Join(dir, "jvm.ring"))
		}},
		// A suffixed name (task 9j4) still names its pid, and the header
		// must name the same one.
		"suffixed, pid mismatch": {uid: uint32(os.Getuid()), alive: true,
			edit: func(t *testing.T, dir string) {
				mustRename(t, filepath.Join(dir, "4242.ring"), filepath.Join(dir, "4241.00ff.ring"))
			}},
		"suffixed, other owner": {uid: uint32(os.Getuid()) + 1, alive: true,
			edit: func(t *testing.T, dir string) {
				mustRename(t, filepath.Join(dir, "4242.ring"), filepath.Join(dir, "4242.00ff.ring"))
			}},
		"empty suffix": {uid: uint32(os.Getuid()), alive: true, edit: func(t *testing.T, dir string) {
			mustRename(t, filepath.Join(dir, "4242.ring"), filepath.Join(dir, "4242..ring"))
		}},
		"two suffixes": {uid: uint32(os.Getuid()), alive: true, edit: func(t *testing.T, dir string) {
			mustRename(t, filepath.Join(dir, "4242.ring"), filepath.Join(dir, "4242.ab.cd.ring"))
		}},
		"suffix not hex": {uid: uint32(os.Getuid()), alive: true, edit: func(t *testing.T, dir string) {
			mustRename(t, filepath.Join(dir, "4242.ring"), filepath.Join(dir, "4242.x-y.ring"))
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			d, dir := testDir(t, c.uid, c.alive)
			d.owner = func(uint64) (uint32, bool) { return c.uid, c.alive }
			if c.edit != nil {
				c.edit(t, dir)
			}
			rings, skipped := scanOnce(t, d)
			if len(rings) != 0 || len(skipped) != 1 || !errors.Is(skipped[0], ErrNotRing) {
				t.Errorf("rings %d, skipped %v", len(rings), skipped)
			}
			if rings, skipped := scanOnce(t, d); len(rings)+len(skipped) != 0 {
				t.Error("rejected file reported again")
			}
		})
	}
}

// TestDirSuffixedRing (task 9j4): a ring the agent had to create as
// <pid>.<hex>.ring — another user planted <pid>.ring in the sticky spool,
// where the agent cannot delete it — is opened like <pid>.ring: the pid
// its name carries passes the owner check before anything is mapped, and
// the header must name it. A suffixed ring whose process is gone is
// removed. The planted file itself needs a second user, so that part is
// a manual check: user B creates <pid>.ring for user A's JVM before the
// attach; A's ring then shows up suffixed and A's Java attribution works.
func TestDirSuffixedRing(t *testing.T) {
	const name = "4242.0123456789abcdef.ring"
	t.Run("alive", func(t *testing.T) {
		d, dir := testDir(t, uint32(os.Getuid()), true)
		mustRename(t, filepath.Join(dir, "4242.ring"), filepath.Join(dir, name))
		rings, skipped := scanOnce(t, d)
		if len(rings) != 1 || len(skipped) != 0 || rings[0].Header().PID != 4242 {
			t.Fatalf("rings %d, skipped %v", len(rings), skipped)
		}
	})
	t.Run("dead", func(t *testing.T) {
		d, dir := testDir(t, uint32(os.Getuid()), false)
		mustRename(t, filepath.Join(dir, "4242.ring"), filepath.Join(dir, name))
		if rings, skipped := scanOnce(t, d); len(rings)+len(skipped) != 0 {
			t.Errorf("rings %d, skipped %v", len(rings), skipped)
		}
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("dead JVM's suffixed ring kept: %v", err)
		}
	})
}

// corruptMagic overwrites a ring file's magic.
func corruptMagic(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteAt([]byte("NOTARING"), 0); err != nil {
		t.Fatal(err)
	}
}

func mustRename(t *testing.T, from, to string) {
	t.Helper()
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
}

// TestMakeSpool: the directory gets the spool mode, an existing one too;
// a file in its place is refused.
func TestMakeSpool(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	for range 2 {
		if err := MakeSpool(dir); err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(dir)
		if err != nil || st.Mode()&(os.ModePerm|os.ModeSticky) != SpoolMode {
			t.Fatalf("mode %v, %v", st.Mode(), err)
		}
	}
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MakeSpool(file); err == nil {
		t.Error("file accepted as spool")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := MakeSpool(link); err == nil {
		t.Error("symlink accepted as spool")
	}
	if _, _, err := NewDir(filepath.Join(t.TempDir(), "missing")).Scan(); err == nil {
		t.Error("missing directory scanned")
	}
}

// TestMakeSpoolAt: the staged spool (decision #14) is created with the
// spool mode, past the umask, and returned open — a Dir reads it through
// /proc/self/fd; anything already in its place (a directory, a symlink to
// another directory, a file) is refused untouched, never adopted.
func TestMakeSpoolAt(t *testing.T) {
	parent := t.TempDir()
	pfd, err := syscall.Open(parent, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(pfd) }()
	old := syscall.Umask(0o077)
	fd, err := MakeSpoolAt(pfd, "spool")
	syscall.Umask(old)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(fd) }()
	st, err := os.Stat(filepath.Join(parent, "spool"))
	if err != nil || st.Mode()&(os.ModePerm|os.ModeSticky) != SpoolMode {
		t.Fatalf("mode %v, %v", st.Mode(), err)
	}
	if _, _, err := NewDir("/proc/self/fd/" + strconv.Itoa(fd)).Scan(); err != nil {
		t.Errorf("scan through the descriptor: %v", err)
	}
	other := t.TempDir()
	if err := os.Symlink(other, filepath.Join(parent, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(parent, "dir"), 0o777); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"spool", "link", "file", "dir"} {
		if fd, err := MakeSpoolAt(pfd, name); !errors.Is(err, syscall.EEXIST) {
			_ = syscall.Close(fd)
			t.Errorf("%s: %v, want EEXIST", name, err)
		}
	}
	if st, err := os.Stat(other); err != nil || st.Mode()&os.ModeSticky != 0 {
		t.Errorf("the symlink's target was chmodded: %v, %v", st.Mode(), err)
	}
}

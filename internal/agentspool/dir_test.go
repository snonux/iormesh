package agentspool

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// testDir is a Dir over the directory of a test ring (4242.ring) whose
// process 4242 belongs to uid, or is gone when alive is false.
func testDir(t *testing.T, uid uint32, alive bool) (*Dir, string) {
	t.Helper()
	path, _ := newTestRing(t, testCap)
	d := NewDir(filepath.Dir(path))
	d.owner = func(pid uint64) (uint32, bool) {
		if pid != 4242 {
			t.Errorf("owner asked for pid %d", pid)
		}
		return uid, alive
	}
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
	if d.done["4243.ring"] {
		t.Error("a ring not set up yet is given up")
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
		"other owner":  {uid: uint32(os.Getuid()) + 1, alive: true},
		"process gone": {uid: uint32(os.Getuid()), alive: false},
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
	if _, _, err := NewDir(filepath.Join(t.TempDir(), "missing")).Scan(); err == nil {
		t.Error("missing directory scanned")
	}
}

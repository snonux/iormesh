package agentspool

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRingRemove (task 2j4): Remove unlinks the file the ring mapped, and
// only that file: a new ring under the same name (a reused pid's JVM
// replaced it) or a symlink planted there stays. The mapping outlives
// the unlink.
func TestRingRemove(t *testing.T) {
	t.Run("own file", func(t *testing.T) {
		path, _ := newTestRing(t, testCap)
		r := openRing(t, path)
		if err := r.Remove(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("ring file still there: %v", err)
		}
		if r.Header().PID != 4242 || r.Pending() != 0 {
			t.Error("mapping lost with the file")
		}
		if err := r.Remove(); err != nil {
			t.Errorf("second remove: %v", err)
		}
	})
	t.Run("replaced by a new ring", func(t *testing.T) {
		path, _ := newTestRing(t, testCap)
		r := openRing(t, path)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		mustRename(t, path, path+".old") // keeps the old inode alive
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := r.Remove(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("another inode's ring removed: %v", err)
		}
	})
	t.Run("symlink planted", func(t *testing.T) {
		path, _ := newTestRing(t, testCap)
		r := openRing(t, path)
		mustRename(t, path, filepath.Join(filepath.Dir(path), "real"))
		if err := os.Symlink("real", path); err != nil {
			t.Fatal(err)
		}
		if err := r.Remove(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("symlink removed: %v", err)
		}
	})
}

// TestDirRemovesDeadRings (task 2j4): a ring whose process is gone is
// unlinked by the scan — the JVM died without its shutdown hook (SIGKILL)
// or before this session — and not reported as skipped; a symlink or a
// hard-linked file under a dead pid's name is left alone, and a live
// process's ring is never removed.
func TestDirRemovesDeadRings(t *testing.T) {
	t.Run("dead", func(t *testing.T) {
		d, dir := testDir(t, uint32(os.Getuid()), false)
		rings, skipped := scanOnce(t, d)
		if len(rings)+len(skipped) != 0 {
			t.Errorf("rings %d, skipped %v", len(rings), skipped)
		}
		if _, err := os.Lstat(filepath.Join(dir, "4242.ring")); !os.IsNotExist(err) {
			t.Errorf("dead JVM's ring kept: %v", err)
		}
	})
	t.Run("alive", func(t *testing.T) {
		d, dir := testDir(t, uint32(os.Getuid()), true)
		if rings, _ := scanOnce(t, d); len(rings) != 1 {
			t.Fatalf("%d rings", len(rings))
		}
		if _, err := os.Lstat(filepath.Join(dir, "4242.ring")); err != nil {
			t.Errorf("live JVM's ring removed: %v", err)
		}
	})
	for name, edit := range map[string]func(t *testing.T, dir string){
		"symlink": func(t *testing.T, dir string) {
			mustRename(t, filepath.Join(dir, "4242.ring"), filepath.Join(dir, "real"))
			if err := os.Symlink("real", filepath.Join(dir, "4242.ring")); err != nil {
				t.Fatal(err)
			}
		},
		"hard link": func(t *testing.T, dir string) {
			if err := os.Link(filepath.Join(dir, "4242.ring"), filepath.Join(dir, "x")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			d, dir := testDir(t, uint32(os.Getuid()), false)
			edit(t, dir)
			scanOnce(t, d)
			if _, err := os.Lstat(filepath.Join(dir, "4242.ring")); err != nil {
				t.Errorf("%s removed: %v", name, err)
			}
		})
	}
}

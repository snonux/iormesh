package servicedb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestOpenCreatesPrivateFile: a new database is 0600 in a 0700
// directory — it holds hosts, addresses and paths.
func TestOpenCreatesPrivateFile(t *testing.T) {
	_, path := openTemp(t)
	for p, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s: mode %v, want %v", p, got, want)
		}
	}
}

// TestOpenReopens: a second Open of the same file keeps the data and
// runs no migration twice.
func TestOpenReopens(t *testing.T) {
	ctx := context.Background()
	d, path := openTemp(t)
	if _, err := d.AddRule(ctx, Rule{HostGlob: "orders*", Port: 8443, Service: "orders"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = again.Close() }()
	rules, err := again.Rules(ctx)
	if err != nil || len(rules) != 1 {
		t.Fatalf("rules %v, err %v: want the one stored before", rules, err)
	}
}

// TestOpenRejects (negative): files that are not ours to read or write.
func TestOpenRejects(t *testing.T) {
	ctx := context.Background()
	for name, c := range openRejectCases(ctx) {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "iormesh")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, FileName)
			c.setup(t, path)
			d, err := Open(ctx, path)
			if err == nil {
				_ = d.Close()
				t.Fatal("opened")
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Errorf("err %v, want %v", err, c.want)
			}
		})
	}
}

// openRejectCase prepares a database path Open must refuse; want is the
// sentinel, nil when only the failure matters (the driver's own error).
type openRejectCase struct {
	setup func(t *testing.T, path string)
	want  error
}

func openRejectCases(ctx context.Context) map[string]openRejectCase {
	return map[string]openRejectCase{
		"not sqlite": {func(t *testing.T, path string) {
			writeFile(t, path, []byte("this is not a database, just text that is long enough"), 0o600)
		}, nil},
		"group readable": {func(t *testing.T, path string) { writeFile(t, path, nil, 0o640) }, ErrUnsafeFile},
		"dir world writable": {func(t *testing.T, path string) {
			if err := os.Chmod(filepath.Dir(path), 0o777); err != nil {
				t.Fatal(err)
			}
		}, ErrUnsafeFile},
		"symlink": {func(t *testing.T, path string) {
			target := filepath.Join(t.TempDir(), "elsewhere.db")
			writeFile(t, target, nil, 0o600)
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}, ErrUnsafeFile},
		"newer schema": {func(t *testing.T, path string) {
			d, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.db.Exec("PRAGMA user_version = 99"); err != nil {
				t.Fatal(err)
			}
			_ = d.Close()
		}, ErrNewerSchema},
	}
}

// TestOpenRejectsQueryPath (negative): the driver would read options
// after '?' and open another file than the one checked.
func TestOpenRejectsQueryPath(t *testing.T) {
	if _, err := Open(context.Background(), filepath.Join(t.TempDir(), "x.db?mode=memory")); err == nil {
		t.Fatal("opened a path with '?'")
	}
}

func writeFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil { // past the umask
		t.Fatal(err)
	}
}

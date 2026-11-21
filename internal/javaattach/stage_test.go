package javaattach

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"iormesh/internal/agentspool"
)

// The staging cases (decision #14, task cf4): the JVM's root is its
// users' to tamper with, and the tracer staging into it is root. The fake
// root is fakeProc's root/ directory, reached through ProcRoot like
// /proc/<pid>/root.

// stageFixture is a fake JVM process and the tracer-side jar to stage.
type stageFixture struct {
	p   fakeProc
	jar string
	// outside is a file beside the fake root that no stage may touch: the
	// host file a planted symlink points at.
	outside string
}

var jarBytes = []byte("PK\x03\x04 the agent")

func newStageFixture(t *testing.T) stageFixture {
	t.Helper()
	p := newFakeProc(t, jvmStatus, jvmMaps, "java\x00")
	f := stageFixture{p: p, jar: filepath.Join(p.root, "agent.jar"),
		outside: filepath.Join(p.root, "host-file")}
	for path, data := range map[string][]byte{f.jar: jarBytes, f.outside: []byte("host")} {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A real /tmp: root's (here the test user's, the JVM's euid), 1777.
	if err := os.Chmod(f.tmp(""), 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f stageFixture) tmp(name string) string { return filepath.Join(f.p.dir, "root/tmp", name) }

func (f stageFixture) stage() (*Stage, error) {
	return f.p.attacher(nil).Stage(uint32(fakePID), f.jar)
}

// names makes stageName return these names in turn.
func names(t *testing.T, list ...string) {
	t.Helper()
	orig := stageName
	stageName = func() (string, error) {
		if len(list) == 0 {
			return "", errors.New("out of names")
		}
		n := list[0]
		list = list[1:]
		return n, nil
	}
	t.Cleanup(func() { stageName = orig })
}

func mode(t *testing.T, path string) (os.FileMode, uint32) {
	t.Helper()
	st, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode(), st.Sys().(*syscall.Stat_t).Uid
}

// TestStage: the directory (0755), the jar (a 0644 copy) and the spool
// (1733) are the tracer's, at the paths the JVM is given; the spool reads
// through the held descriptor; Remove deletes all of it — rings and
// stray entries too, a planted symlink unlinked, not followed.
func TestStage(t *testing.T) {
	f := newStageFixture(t)
	names(t, "iormesh-a")
	s, err := f.stage()
	if err != nil {
		t.Fatal(err)
	}
	if s.Jar() != "/tmp/iormesh-a/iormesh-agent.jar" || s.Spool() != "/tmp/iormesh-a/spool" {
		t.Errorf("paths %s, %s", s.Jar(), s.Spool())
	}
	euid := uint32(os.Geteuid())
	for path, want := range map[string]os.FileMode{
		f.tmp("iormesh-a"):                   os.ModeDir | 0o755,
		f.tmp("iormesh-a/iormesh-agent.jar"): 0o644,
		f.tmp("iormesh-a/spool"):             os.ModeDir | agentspool.SpoolMode,
	} {
		if m, uid := mode(t, path); m != want || uid != euid {
			t.Errorf("%s: mode %v uid %d, want %v uid %d", path, m, uid, want, euid)
		}
	}
	if data, err := os.ReadFile(f.tmp("iormesh-a/iormesh-agent.jar")); err != nil || !bytes.Equal(data, jarBytes) {
		t.Errorf("staged jar %q, %v", data, err)
	}
	// What the agent and a hostile user leave in the spool.
	spool := f.tmp("iormesh-a/spool")
	if err := os.WriteFile(filepath.Join(spool, "7.ring"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.outside, filepath.Join(spool, "8.ring")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(spool, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(s.ReadPath())
	if err != nil || len(entries) != 3 {
		t.Errorf("spool through %s: %v, %v", s.ReadPath(), entries, err)
	}
	if err := s.Remove(); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(); err != nil {
		t.Errorf("second Remove: %v", err)
	}
	if _, err := os.Lstat(f.tmp("iormesh-a")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stage left behind: %v", err)
	}
	if data, err := os.ReadFile(f.outside); err != nil || string(data) != "host" {
		t.Errorf("the symlink's target was touched: %q, %v", data, err)
	}
}

// TestStageSkipsPlantedNames: a name already taken in /tmp — a directory,
// a symlink to the host's tree, a file — is never adopted or touched; the
// next fresh name is used, and when every try is taken nothing is staged.
func TestStageSkipsPlantedNames(t *testing.T) {
	f := newStageFixture(t)
	hostDir := filepath.Join(f.p.root, "host-dir")
	if err := os.Mkdir(hostDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(f.tmp("iormesh-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	chmod(t, f.tmp("iormesh-dir"), 0o777)
	if err := os.Symlink(hostDir, f.tmp("iormesh-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.tmp("iormesh-file"), nil, 0o666); err != nil {
		t.Fatal(err)
	}
	names(t, "iormesh-dir", "iormesh-link", "iormesh-file", "iormesh-fresh")
	s, err := f.stage()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Remove() }()
	if s.Jar() != "/tmp/iormesh-fresh/iormesh-agent.jar" {
		t.Errorf("staged at %s, want the fresh name", s.Jar())
	}
	if entries, _ := os.ReadDir(hostDir); len(entries) != 0 {
		t.Errorf("staged through the planted symlink: %v", entries)
	}
	if entries, _ := os.ReadDir(f.tmp("iormesh-dir")); len(entries) != 0 {
		t.Errorf("staged into the planted directory: %v", entries)
	}
	if m, _ := mode(t, f.tmp("iormesh-dir")); m.Perm() != 0o777 {
		t.Errorf("planted directory's mode changed to %v", m)
	}

	all := make([]string, stageTries)
	for i := range all {
		all[i] = "iormesh-dir"
	}
	names(t, all...)
	if _, err := f.stage(); !errors.Is(err, ErrNoStage) {
		t.Errorf("every name taken: err %v, want ErrNoStage", err)
	}
}

// TestStageRefuses: a root the stage could be swapped in, a /tmp that is
// a symlink or missing, or a process that is no JVM gets no stage — and
// nothing is created anywhere.
func TestStageRefuses(t *testing.T) {
	cases := map[string]struct {
		edit func(t *testing.T, f stageFixture)
		want error
	}{
		"tmp not sticky": {func(t *testing.T, f stageFixture) { chmod(t, f.tmp(""), 0o777) }, ErrUntrustedTmp},
		"tmp group-writable": {func(t *testing.T, f stageFixture) { chmod(t, f.tmp(""), 0o775) },
			ErrUntrustedTmp},
		"root world-writable": {func(t *testing.T, f stageFixture) {
			chmod(t, filepath.Join(f.p.dir, "root"), 0o777)
		}, ErrUntrustedTmp},
		"tmp a symlink": {func(t *testing.T, f stageFixture) {
			hostTmp := filepath.Join(f.p.root, "host-tmp")
			if err := os.Rename(f.tmp(""), hostTmp); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(hostTmp, f.tmp("")); err != nil {
				t.Fatal(err)
			}
		}, syscall.ELOOP},
		"no tmp": {func(t *testing.T, f stageFixture) {
			if err := os.Remove(f.tmp("")); err != nil {
				t.Fatal(err)
			}
		}, syscall.ENOENT},
		"tmp read-only": {func(t *testing.T, f stageFixture) {
			if os.Geteuid() == 0 {
				t.Skip("root writes a 0555 directory anyway")
			}
			chmod(t, f.tmp(""), 0o555)
		}, ErrNoStage},
		"not a JVM": {func(t *testing.T, f stageFixture) {
			if err := os.WriteFile(filepath.Join(f.p.dir, "maps"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}, ErrNotJVM},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newStageFixture(t)
			c.edit(t, f)
			names(t, "iormesh-a")
			if _, err := f.stage(); !errors.Is(err, c.want) {
				t.Fatalf("Stage: %v, want %v", err, c.want)
			}
			assertNothingStaged(t, f)
		})
	}
}

// TestStageRefusesForeignOwner: a /tmp of a third user — neither root
// nor the JVM's — could be emptied by that user under the JVM. Run as
// a non-root user, the fixture's /tmp is that user's and the JVM another
// one's.
func TestStageRefusesForeignOwner(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("as root the fixture's /tmp is root's, which is trusted")
	}
	f := newStageFixture(t)
	if err := os.WriteFile(filepath.Join(f.p.dir, "status"), []byte(statusOf(fakePID, os.Geteuid()+1)),
		0o644); err != nil {
		t.Fatal(err)
	}
	names(t, "iormesh-a")
	if _, err := f.stage(); !errors.Is(err, ErrUntrustedTmp) || !strings.Contains(err.Error(), "uid") {
		t.Fatalf("Stage: %v, want ErrUntrustedTmp naming the owner", err)
	}
	assertNothingStaged(t, f)
}

func chmod(t *testing.T, path string, m os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, m); err != nil {
		t.Fatal(err)
	}
}

// assertNothingStaged: no iormesh-* entry in the fixture's /tmp, wherever
// it points, nor in the tree around the fake root.
func assertNothingStaged(t *testing.T, f stageFixture) {
	t.Helper()
	err := filepath.WalkDir(f.p.root, func(path string, d os.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), "iormesh-") {
			t.Errorf("staged anyway: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestCopyJarRefusesPlanted: the jar is created, never written through an
// existing file or a symlink (to the host's tree).
func TestCopyJarRefusesPlanted(t *testing.T) {
	f := newStageFixture(t)
	dir := f.tmp("")
	if err := os.Symlink(f.outside, filepath.Join(dir, "link.jar")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file.jar"), []byte("planted"), 0o666); err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(fd) }()
	for _, name := range []string{"link.jar", "file.jar"} {
		if err := copyJar(fd, name, f.jar); !errors.Is(err, syscall.EEXIST) {
			t.Errorf("%s: %v, want EEXIST", name, err)
		}
	}
	if data, _ := os.ReadFile(f.outside); string(data) != "host" {
		t.Errorf("written through the symlink: %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "file.jar")); string(data) != "planted" {
		t.Errorf("planted file overwritten: %q", data)
	}
}

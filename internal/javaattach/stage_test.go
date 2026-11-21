package javaattach

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
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
	// The tracer's and the JVM's PID namespace: the same.
	nsLink(t, filepath.Join(p.root, "self"), tracerPidNS)
	nsLink(t, p.dir, tracerPidNS)
	return f
}

const tracerPidNS = "pid:[4026531836]"

// nsLink makes dir/ns/pid name the PID namespace ns, as /proc's does.
func nsLink(t *testing.T, dir, ns string) {
	t.Helper()
	link := filepath.Join(dir, "ns", "pid")
	_ = os.Remove(link)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ns, link); err != nil {
		t.Fatal(err)
	}
}

// addProc adds another fake JVM, pid, of uid euid, to the fixture's
// /proc: its root is the first JVM's (sharedRoot) or a fresh one with a
// /tmp of its own.
func (f stageFixture) addProc(t *testing.T, pid, euid int, sharedRoot bool) {
	t.Helper()
	dir := filepath.Join(f.p.root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"status": statusOf(pid, euid), "maps": jvmMaps} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	nsLink(t, dir, tracerPidNS)
	if sharedRoot {
		if err := os.Symlink(filepath.Join(f.p.dir, "root"), filepath.Join(dir, "root")); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.MkdirAll(filepath.Join(dir, "root/tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	chmod(t, filepath.Join(dir, "root/tmp"), 0o777|os.ModeSticky)
}

func (f stageFixture) tmp(name string) string { return filepath.Join(f.p.dir, "root/tmp", name) }

func (f stageFixture) stage() (*Stage, error) {
	return f.p.attacher(nil).Stage(uint32(fakePID), f.jar, nil)
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
		// A host tracer and a container JVM: the agent's 1.ring would
		// be read as the tracer's pid 1.
		"other PID namespace": {func(t *testing.T, f stageFixture) {
			nsLink(t, f.p.dir, "pid:[4026532999]")
		}, ErrOtherPidNS},
		"pid gone": {func(t *testing.T, f stageFixture) {
			if err := os.RemoveAll(f.p.dir); err != nil {
				t.Fatal(err)
			}
		}, syscall.ENOENT},
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

// TestStageReuse (review B1, N1): a JVM shares a stage only with the same
// euid and the same /tmp directory object. One of the same user in the
// same root gets the first JVM's stage; one with a root of its own (a
// chroot in the same mount namespace) a stage of its own; and one of
// another user is checked afresh — in a /tmp the first JVM's user owns,
// that user could swap the jar under it, so it is refused, never handed
// the stage.
func TestStageReuse(t *testing.T) {
	f := newStageFixture(t)
	a := f.p.attacher(nil)
	names(t, "iormesh-a", "iormesh-b", "iormesh-c")
	first, err := a.Stage(uint32(fakePID), f.jar, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Remove() }()
	f.addProc(t, fakePID+1, os.Geteuid(), true)
	f.addProc(t, fakePID+2, os.Geteuid(), false)
	same, err := a.Stage(uint32(fakePID+1), f.jar, []*Stage{first})
	if err != nil || same != first {
		t.Errorf("same user and /tmp: stage %p, %v; want the first, %p", same, err, first)
	}
	own, err := a.Stage(uint32(fakePID+2), f.jar, []*Stage{first})
	if err != nil || own == first {
		t.Fatalf("own root: stage %p, %v; want a new one", own, err)
	}
	defer func() { _ = own.Remove() }()
	if own.Jar() != "/tmp/iormesh-b/iormesh-agent.jar" {
		t.Errorf("own root staged at %s", own.Jar())
	}
	if _, err := os.Stat(filepath.Join(f.p.root, strconv.Itoa(fakePID+2), "root/tmp/iormesh-b/spool")); err != nil {
		t.Errorf("own root's stage not in its /tmp: %v", err)
	}
	if os.Geteuid() == 0 {
		return // as root the fixture's /tmp is root's, trusted for every user
	}
	f.addProc(t, fakePID+3, os.Geteuid()+1, true)
	if s, err := a.Stage(uint32(fakePID+3), f.jar, []*Stage{first, own}); !errors.Is(err, ErrUntrustedTmp) {
		t.Errorf("another user in the first user's /tmp: stage %p, %v; want ErrUntrustedTmp", s, err)
	}
}

// TestStageReuseOtherUser (review B1): in a /tmp every user trusts
// (root's, played here by the test's own uid), a JVM of another user
// passes the checks but still gets a stage of its own, never the first
// user's.
func TestStageReuseOtherUser(t *testing.T) {
	f := newStageFixture(t)
	orig := rootUID
	rootUID = uint32(os.Geteuid())
	t.Cleanup(func() { rootUID = orig })
	a := f.p.attacher(nil)
	names(t, "iormesh-a", "iormesh-b")
	first, err := a.Stage(uint32(fakePID), f.jar, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Remove() }()
	f.addProc(t, fakePID+1, os.Geteuid()+1, true)
	other, err := a.Stage(uint32(fakePID+1), f.jar, []*Stage{first})
	if err != nil || other == first {
		t.Fatalf("another user, same /tmp: stage %p, %v; want a new one", other, err)
	}
	defer func() { _ = other.Remove() }()
	if other.Jar() != "/tmp/iormesh-b/iormesh-agent.jar" {
		t.Errorf("staged at %s", other.Jar())
	}
}

// TestStageRefusesSwappedDir (review N3): a stage directory found not
// to be the tracer's right after mkdirat — swapped for another user's —
// is refused and never touched again, Remove included.
func TestStageRefusesSwappedDir(t *testing.T) {
	f := newStageFixture(t)
	orig := tracerEUID
	tracerEUID = func() int { return os.Geteuid() + 1 }
	t.Cleanup(func() { tracerEUID = orig })
	names(t, "iormesh-a")
	if _, err := f.stage(); !errors.Is(err, ErrUntrustedTmp) {
		t.Fatalf("Stage: %v, want ErrUntrustedTmp", err)
	}
	m, _ := mode(t, f.tmp("iormesh-a"))
	if entries, _ := os.ReadDir(f.tmp("iormesh-a")); len(entries) != 0 || m.Perm() != 0o700 {
		t.Errorf("the swapped directory was written to or chmodded: %v, %v", entries, m)
	}
}

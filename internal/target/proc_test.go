package target

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// fakeProc is a /proc tree in a temp dir.
type fakeProc struct {
	t    *testing.T
	root string
}

func newFakeProc(t *testing.T) fakeProc {
	return fakeProc{t: t, root: t.TempDir()}
}

func (f fakeProc) fs() ProcFS { return ProcFS{Root: f.root} }

// proc adds a process: stat with state, ppid and start ticks, status with
// tgid, cmdline from argv and an exe link.
func (f fakeProc) proc(pid, tgid, ppid uint32, state byte, ticks uint64, exe string, argv ...string) {
	f.t.Helper()
	dir := filepath.Join(f.root, strconv.Itoa(int(pid)))
	f.write(dir, "stat", fmt.Sprintf("%d (a (b) c) %c %d 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 %d 0 0",
		pid, state, ppid, ticks))
	f.write(dir, "status", fmt.Sprintf("Name:\tx\nTgid:\t%d\nPid:\t%d\n", tgid, pid))
	var cmd []byte
	for _, a := range argv {
		cmd = append(append(cmd, a...), 0)
	}
	f.write(dir, "cmdline", string(cmd))
	if exe != "" {
		if err := os.Symlink(exe, filepath.Join(dir, "exe")); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f fakeProc) write(dir, name, content string) {
	f.t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f fakeProc) socket(pid uint32, fd int, inode string) {
	f.t.Helper()
	dir := filepath.Join(f.root, strconv.Itoa(int(pid)), "fd")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Symlink("socket:["+inode+"]", filepath.Join(dir, strconv.Itoa(fd))); err != nil {
		f.t.Fatal(err)
	}
}

func TestParseStat(t *testing.T) {
	st, err := ParseStat([]byte("42 (we ird) comm)) S 7 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 12345 0 0"))
	if err != nil || st.State != 'S' || st.PPID != 7 || st.Flags != 0 || st.StartNS != 12345*10_000_000 {
		t.Fatalf("ParseStat = %+v, %v", st, err)
	}
	if st.ArgStart != 0 {
		t.Fatalf("a short line has arg_start %d", st.ArgStart)
	}
	// A full 52-field line: arg_start is field 48.
	full := "42 (x) S 7" + strings.Repeat(" 0", 17) + " 12345" + strings.Repeat(" 0", 25) + " 140737" + strings.Repeat(" 0", 4)
	if st, err := ParseStat([]byte(full)); err != nil || st.StartNS != 12345*10_000_000 || st.ArgStart != 140737 {
		t.Fatalf("ParseStat(full) = %+v, %v", st, err)
	}
	for _, bad := range []string{"", "42 no paren", "42 (x) S 7", "42 (x) S x 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 1"} {
		if _, err := ParseStat([]byte(bad)); err == nil {
			t.Errorf("ParseStat(%q) succeeded", bad)
		}
	}
}

// TestAlive: liveness is more than /proc/<pid> existing — a reused PID
// (other start time), a zombie and a thread id are all not the target.
func TestAlive(t *testing.T) {
	f := newFakeProc(t)
	f.proc(10, 10, 1, 'S', 500, "")
	f.proc(11, 11, 1, 'Z', 500, "")
	f.proc(12, 10, 1, 'S', 500, "") // a thread of 10
	f.proc(14, 14, 1, 'S', 500, "")
	// 14 is in do_exit: state still S, but PF_EXITING (0x4) set.
	f.write(filepath.Join(f.root, "14"), "stat",
		"14 (a (b) c) S 1 1 1 0 -1 4194308 0 0 0 0 0 0 0 0 20 0 1 0 500 0 0")
	fs := f.fs()
	cases := []struct {
		pid   uint32
		start uint64
		want  bool
	}{
		{10, 500 * 10_000_000, true},
		{10, 0, true},
		{10, 499 * 10_000_000, false}, // PID reused since
		{11, 0, false},                // zombie
		{12, 0, false},                // thread id
		{13, 0, false},                // gone
		{14, 0, false},                // exiting, not yet a zombie
	}
	for _, c := range cases {
		if got := fs.Alive(c.pid, c.start); got != c.want {
			t.Errorf("Alive(%d, %d) = %v, want %v", c.pid, c.start, got, c.want)
		}
	}
}

// TestResolvePattern: running matches are found; the tracer, its
// ancestors and other iormesh sessions (whose command lines name the
// pattern) are skipped.
func TestResolvePattern(t *testing.T) {
	f := newFakeProc(t)
	f.proc(1, 1, 0, 'S', 1, "/sbin/init", "/sbin/init")
	f.proc(50, 50, 1, 'S', 2, "/usr/bin/sudo", "sudo", "iormesh", "run", "-pattern", "*RestartApp*")
	f.proc(51, 51, 50, 'S', 3, "/build/iormesh", "iormesh", "run", "-pattern", "*RestartApp*")
	f.proc(60, 60, 1, 'S', 4, "/usr/bin/java", "java", "-cp", "w.jar", "x.RestartApp", "/d")
	f.proc(61, 61, 1, 'S', 5, "/usr/bin/java", "java", "-jar", "w.jar", "restart")
	// A second iormesh session, not an ancestor: its argv names the
	// pattern, its executable is the tracer's.
	f.proc(70, 70, 1, 'S', 6, "/build/iormesh", "iormesh", "run", "-pattern", "*RestartApp*")
	got, err := Spec{Pattern: "*RestartApp*"}.Resolve(f.fs(), 51)
	if err != nil || !slices.Equal(got, []uint32{60}) {
		t.Fatalf("Resolve = %v, %v; want [60]", got, err)
	}
}

func TestResolvePIDs(t *testing.T) {
	f := newFakeProc(t)
	f.proc(20, 20, 1, 'S', 1, "")
	f.proc(21, 20, 1, 'S', 1, "")
	if got, err := (Spec{PIDs: []uint32{20}}).Resolve(f.fs(), 1); err != nil || !slices.Equal(got, []uint32{20}) {
		t.Fatalf("Resolve = %v, %v", got, err)
	}
	for _, pid := range []uint32{21, 22} {
		if _, err := (Spec{PIDs: []uint32{pid}}).Resolve(f.fs(), 1); err == nil {
			t.Errorf("pid %d (a thread / gone) resolved", pid)
		}
	}
}

// TestListenOwners: the LISTEN socket on the port (IPv4 or IPv6) names
// its owner; an established socket on the same port does not.
func TestListenOwners(t *testing.T) {
	f := newFakeProc(t)
	header := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	f.write(filepath.Join(f.root, "net"), "tcp", header+
		"   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 111 1\n"+
		"   1: 0100007F:1F90 0100007F:D000 01 00000000:00000000 00:00000000 00000000  1000        0 222 1\n")
	f.write(filepath.Join(f.root, "net"), "tcp6", header+
		"   0: 00000000000000000000000000000000:1F91 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 333 1\n")
	f.proc(30, 30, 1, 'S', 1, "")
	f.socket(30, 5, "111")
	f.proc(31, 31, 1, 'S', 1, "")
	f.socket(31, 7, "222")
	f.proc(32, 32, 1, 'S', 1, "")
	f.socket(32, 3, "333")
	if got, err := f.fs().ListenOwners(8080); err != nil || !slices.Equal(got, []uint32{30}) {
		t.Fatalf("ListenOwners(8080) = %v, %v; want [30]", got, err)
	}
	if got, err := f.fs().ListenOwners(8081); err != nil || !slices.Equal(got, []uint32{32}) {
		t.Fatalf("ListenOwners(8081) = %v, %v; want [32]", got, err)
	}
	if _, err := f.fs().ListenOwners(9); err == nil {
		t.Fatal("a port nothing listens on resolved")
	}
}

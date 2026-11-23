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
	if err != nil || st.Comm != "we ird) comm)" || st.State != 'S' || st.PPID != 7 || st.Flags != 0 || st.StartNS != 12345*10_000_000 {
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

// TestCommand (task nh4): a running process's argv and executable are
// read for its attach event only while it is still the incarnation the
// session took on; a reused PID, a process gone, or a kernel thread
// (empty cmdline, no exe link) yields neither, and so does an unknown
// start time (nothing to check the read against).
func TestCommand(t *testing.T) {
	f := newFakeProc(t)
	f.proc(10, 10, 1, 'S', 500, "/usr/bin/java", "java", "-jar", "app.jar")
	f.proc(11, 11, 2, 'S', 500, "")
	fs := f.fs()
	cases := []struct {
		name     string
		pid      uint32
		start    uint64
		wantArgv []string
		wantExe  string
	}{
		{"same incarnation", 10, 500 * 10_000_000, []string{"java", "-jar", "app.jar"}, "/usr/bin/java"},
		{"PID reused since", 10, 499 * 10_000_000, nil, ""},
		{"unknown start", 10, 0, nil, ""},
		{"kernel thread", 11, 500 * 10_000_000, nil, ""},
		{"gone", 13, 500 * 10_000_000, nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			argv, exe, cut := fs.Command(c.pid, c.start)
			if !slices.Equal(argv, c.wantArgv) || exe != c.wantExe || cut {
				t.Errorf("Command(%d, %d) = %q, %q, %v; want %q, %q, false", c.pid, c.start, argv, exe, cut,
					c.wantArgv, c.wantExe)
			}
		})
	}
}

// TestCmdlineBounded (review of task nh4): a command line is read up to
// MaxCmdline bytes; a longer one is cut there and says so, one exactly
// that long is whole.
func TestCmdlineBounded(t *testing.T) {
	f := newFakeProc(t)
	exact := strings.Repeat("a", MaxCmdline-1) // plus its NUL: MaxCmdline
	f.proc(10, 10, 1, 'S', 500, "", exact)
	f.proc(11, 11, 1, 'S', 500, "", "java", "-cp", strings.Repeat("b", 2*MaxCmdline))
	fs := f.fs()
	if argv, cut, err := fs.CmdlineBounded(10); err != nil || cut || len(argv) != 1 || argv[0] != exact {
		t.Errorf("exact: %d elements, cut %v, err %v; want whole", len(argv), cut, err)
	}
	argv, cut, err := fs.CmdlineBounded(11)
	if err != nil || !cut || len(argv) != 3 || len(strings.Join(argv, "\x00")) != MaxCmdline {
		t.Errorf("long: %d elements, cut %v, err %v; want cut at %d bytes", len(argv), cut, err, MaxCmdline)
	}
	if _, _, err := fs.CmdlineBounded(12); err == nil {
		t.Error("gone: no error")
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

// TestListenHoldersStartTime (task qj4): each holder comes with the start
// time read with its fd table, so a later adoption can tell the listed
// incarnation from a process that reused its pid.
func TestListenHoldersStartTime(t *testing.T) {
	f := newFakeProc(t)
	f.listenTable()
	f.proc(30, 30, 1, 'S', 7, "/usr/bin/app")
	f.socket(30, 5, "111")
	st, err := f.fs().Stat(30)
	if err != nil || st.StartNS == 0 {
		t.Fatalf("Stat(30) = %+v, %v", st, err)
	}
	got, err := f.fs().ListenHolders(8080)
	if err != nil || !slices.Equal(got, []Holder{{PID: 30, StartNS: st.StartNS}}) {
		t.Fatalf("ListenHolders(8080) = %v, %v; want pid 30 at %d", got, err, st.StartNS)
	}
}

// listenTable is a /proc/net/tcp with one LISTEN socket on 8080, inode
// 111.
func (f fakeProc) listenTable() {
	f.t.Helper()
	header := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	f.write(filepath.Join(f.root, "net"), "tcp", header+
		"   0: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 111 1\n")
}

// TestListenOwnersSkipsServiceManager (task mh4): a socket-activated
// listener is held by systemd (pid 1, or a user manager) and by the
// service it started; only the service is an owner. A port only the
// manager holds is an error that says so: tracing the manager would
// follow every service it spawns.
func TestListenOwnersSkipsServiceManager(t *testing.T) {
	f := newFakeProc(t)
	f.listenTable()
	f.proc(1, 1, 0, 'S', 1, "/usr/lib/systemd/systemd")
	f.socket(1, 40, "111")
	f.proc(900, 900, 800, 'S', 1, "/usr/lib/systemd/systemd")
	f.socket(900, 41, "111")
	if _, err := f.fs().ListenOwners(8080); err == nil || !strings.Contains(err.Error(), "service manager") {
		t.Fatalf("only the manager holds the port: err %v, want a service manager error", err)
	}
	f.proc(50, 50, 1, 'S', 1, "/usr/bin/app")
	f.socket(50, 3, "111")
	if got, err := f.fs().ListenOwners(8080); err != nil || !slices.Equal(got, []uint32{50}) {
		t.Fatalf("ListenOwners(8080) = %v, %v; want [50], never the manager", got, err)
	}
}

// TestOwner (task mh4): a process's owner key is its executable — a
// replaced binary's " (deleted)" suffix dropped — and its effective uid;
// a process without an exe link or status has none.
func TestOwner(t *testing.T) {
	f := newFakeProc(t)
	f.proc(50, 50, 1, 'S', 1, "/usr/bin/app (deleted)")
	f.write(filepath.Join(f.root, "50"), "status", "Name:\tapp\nTgid:\t50\nPid:\t50\nUid:\t0\t1000\t1000\t1000\n")
	got, err := f.fs().Owner(50)
	if err != nil || got != (Owner{Exe: "/usr/bin/app", UID: 1000}) {
		t.Fatalf("Owner(50) = %+v, %v; want /usr/bin/app uid 1000", got, err)
	}
	f.proc(51, 51, 1, 'S', 1, "")
	if _, err := f.fs().Owner(51); err == nil {
		t.Fatal("a process without an exe link has an owner key")
	}
	f.proc(52, 52, 1, 'S', 1, "/usr/bin/app")
	if _, err := f.fs().Owner(52); err == nil {
		t.Fatal("a process without a Uid line has an owner key")
	}
	if _, err := f.fs().Owner(53); err == nil {
		t.Fatal("a missing process has an owner key")
	}
}

// TestOwnerCgroup (task kk4): the owner key carries the process's
// cgroup — the cgroup v2 path, else the v1 name=systemd one — and none
// when the file is missing or names only the root cgroup, which ties
// nothing together.
func TestOwnerCgroup(t *testing.T) {
	f := newFakeProc(t)
	status := "Name:\tapp\nUid:\t1000\t1000\t1000\t1000\n"
	cases := []struct {
		cgroup, want string
	}{
		{"0::/system.slice/app.service\n", "/system.slice/app.service"},
		{"12:cpu,cpuacct:/system.slice/app.service\n1:name=systemd:/system.slice/app.service\n0::/\n",
			"/system.slice/app.service"},
		{"0::/\n", ""},
		{"12:cpu,cpuacct:/\n1:name=systemd:/\n", ""}, // v1 only, root
		{"0::/system.slice/app.service\n0::/user.slice\n", "/system.slice/app.service"},
		{"", ""},
	}
	for i, tc := range cases {
		pid := uint32(60 + i)
		f.proc(pid, pid, 1, 'S', 1, "/usr/bin/app")
		dir := filepath.Join(f.root, strconv.Itoa(int(pid)))
		f.write(dir, "status", status)
		if tc.cgroup != "" {
			f.write(dir, "cgroup", tc.cgroup)
		}
		got, err := f.fs().Owner(pid)
		if err != nil || got != (Owner{Exe: "/usr/bin/app", UID: 1000, Cgroup: tc.want}) {
			t.Fatalf("cgroup %q: Owner = %+v, %v; want cgroup %q", tc.cgroup, got, err, tc.want)
		}
	}
}

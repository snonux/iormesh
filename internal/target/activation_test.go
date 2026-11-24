package target

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

// cgroup gives pid a cgroup v2 path.
func (f fakeProc) cgroup(pid uint32, path string) {
	f.t.Helper()
	f.write(filepath.Join(f.root, strconv.Itoa(int(pid))), "cgroup", "0::"+path+"\n")
}

// uid gives pid an effective uid: its status rewritten with a Uid line.
func (f fakeProc) uid(pid, uid uint32) {
	f.t.Helper()
	f.write(filepath.Join(f.root, strconv.Itoa(int(pid))), "status",
		fmt.Sprintf("Name:\tx\nTgid:\t%d\nPid:\t%d\nUid:\t%d\t%d\t%d\t%d\n", pid, pid, uid, uid, uid, uid))
}

// holderPIDs lists the holders' pids.
func holderPIDs(hs []Holder) []uint32 {
	var out []uint32
	for _, h := range hs {
		out = append(out, h.PID)
	}
	return out
}

// TestListenHoldersManagerOnly (task lk4): a port only the service
// manager holds is ErrManagerOnly, still a spec error, so a port session
// can tell it from a port nothing listens on.
func TestListenHoldersManagerOnly(t *testing.T) {
	f := newFakeProc(t)
	f.listenTable()
	f.proc(1, 1, 0, 'S', 1, "/usr/lib/systemd/systemd")
	f.socket(1, 40, "111")
	_, err := f.fs().ListenHolders(8080)
	if !errors.Is(err, ErrManagerOnly) || !errors.Is(err, ErrSpec) {
		t.Fatalf("manager only: err %v, want ErrManagerOnly and ErrSpec", err)
	}
	if _, err := f.fs().ListenHolders(9); errors.Is(err, ErrManagerOnly) {
		t.Fatalf("nothing listens: err %v is ErrManagerOnly", err)
	}
}

// TestActivatedHoldersSystem (task lk4, decision #23): with pid 1 the
// only holder at start, the session pins its listening socket. A later
// holder is the activated service only if it holds that very socket, is
// no manager and runs in a .service unit (or a delegated sub-cgroup of
// one); a login-session process with a copy of the fd, a holder with no
// readable cgroup and a manager in a unit are refused, a holder of
// another socket on the port is not even listed, and a pinned socket that
// no longer listens on the port pins nothing.
func TestActivatedHoldersSystem(t *testing.T) {
	f := newFakeProc(t)
	f.listenTable()
	f.proc(1, 1, 0, 'S', 1, "/usr/lib/systemd/systemd")
	f.cgroup(1, "/init.scope")
	f.socket(1, 40, "111")
	fs := f.fs()
	a, err := fs.ManagerListener(8080)
	if err != nil {
		t.Fatalf("ManagerListener: %v", err)
	}
	if adopt, refuse, err := fs.ActivatedHolders(a); err != nil || len(adopt)+len(refuse) != 0 {
		t.Fatalf("idle: adopt %v refuse %v err %v; want nothing", adopt, refuse, err)
	}

	f.proc(50, 50, 1, 'S', 2, "/usr/bin/app")
	f.cgroup(50, "/system.slice/app.service")
	f.socket(50, 3, "111")
	f.proc(51, 51, 40, 'S', 2, "/usr/bin/bash")
	f.cgroup(51, "/user.slice/user-1000.slice/session-3.scope")
	f.socket(51, 3, "111")
	f.proc(52, 52, 50, 'S', 2, "/usr/bin/app")
	f.cgroup(52, "/system.slice/app.service/payload")
	f.socket(52, 3, "111")
	f.proc(53, 53, 1, 'S', 2, "/usr/bin/other")
	f.cgroup(53, "/system.slice/other.service")
	f.socket(53, 3, "999") // its own socket, not the manager's
	f.proc(54, 54, 1, 'S', 2, "/usr/lib/systemd/systemd")
	f.cgroup(54, "/system.slice/nested.service")
	f.socket(54, 3, "111")
	f.proc(55, 55, 1, 'S', 2, "/usr/bin/app") // no cgroup file
	f.socket(55, 3, "111")
	f.proc(56, 56, 1, 'S', 2, "/usr/bin/app")
	f.cgroup(56, "/system.slice")
	f.socket(56, 3, "111")
	f.proc(57, 57, 1, 'S', 2, "/usr/bin/app") // a look-alike unit name
	f.cgroup(57, "/system.slice/x.service-evil")
	f.socket(57, 3, "111")
	adopt, refuse, err := fs.ActivatedHolders(a)
	if err != nil || !slices.Equal(holderPIDs(adopt), []uint32{50, 52}) ||
		!slices.Equal(holderPIDs(refuse), []uint32{51, 55, 56, 57}) {
		t.Fatalf("adopt %v refuse %v err %v; want adopt [50 52], refuse [51 55 56 57]",
			holderPIDs(adopt), holderPIDs(refuse), err)
	}
	if adopt[0].StartNS == 0 {
		t.Fatalf("holder 50 without a start time: %+v", adopt[0])
	}

	// The socket closed and its inode now names another one: the port's
	// listener is 112, which the manager never held.
	header := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	f.write(filepath.Join(f.root, "net"), "tcp", header+
		"   0: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 112 1\n")
	if adopt, refuse, err := fs.ActivatedHolders(a); err != nil || len(adopt)+len(refuse) != 0 {
		t.Fatalf("pinned socket gone: adopt %v refuse %v err %v; want nothing", adopt, refuse, err)
	}
}

// userManager adds pid 900, the user manager of uid 1000, holding the
// listener 111 from its init.scope below um, and pins it.
func userManager(t *testing.T, f fakeProc, um string) Activation {
	t.Helper()
	f.proc(900, 900, 1, 'S', 1, "/usr/lib/systemd/systemd")
	f.uid(900, 1000)
	f.cgroup(900, um+"/init.scope")
	f.socket(900, 40, "111")
	a, err := f.fs().ManagerListener(8080)
	if err != nil {
		t.Fatalf("ManagerListener: %v", err)
	}
	return a
}

// TestActivatedHoldersUserManager (tasks lk4, el4): a user manager's
// services run below its user@UID.service under the manager's uid; a
// .service elsewhere — pid 1's, another user manager's
// (user@10000.service against user@1000), a look-alike unit name
// (user@1000.service-evil) — a scope, and a service of the manager run
// under another uid (a setuid binary, unreadable status) are not one of
// them.
func TestActivatedHoldersUserManager(t *testing.T) {
	f := newFakeProc(t)
	f.listenTable()
	um := "/user.slice/user-1000.slice/user@1000.service"
	a := userManager(t, f, um)
	holder := func(pid, uid uint32, cg string) {
		f.proc(pid, pid, 900, 'S', 2, "/usr/bin/app")
		if uid != 0 {
			f.uid(pid, uid)
		}
		f.cgroup(pid, cg)
		f.socket(pid, 3, "111")
	}
	holder(60, 1000, um+"/app.slice/app.service")
	holder(61, 1000, "/system.slice/app.service")
	holder(62, 1000, um+"/app.slice/app-term.scope")
	holder(63, 10000, "/user.slice/user-10000.slice/user@10000.service/app.slice/app.service")
	holder(64, 1000, "/user.slice/user-1000.slice/user@1000.service-evil/app.service")
	holder(65, 1001, um+"/app.slice/app.service")
	holder(66, 0, um+"/app.slice/app.service") // status without a Uid line
	adopt, refuse, err := f.fs().ActivatedHolders(a)
	if err != nil || !slices.Equal(holderPIDs(adopt), []uint32{60}) ||
		!slices.Equal(holderPIDs(refuse), []uint32{61, 62, 63, 64, 65, 66}) {
		t.Fatalf("adopt %v refuse %v err %v; want adopt [60], refuse [61 62 63 64 65 66]",
			holderPIDs(adopt), holderPIDs(refuse), err)
	}
}

// TestManagerListenerUserManagerRoot (task el4): a user manager's units
// run below its own user@UID.service, the parent of its init.scope, with
// UID its own uid; a manager in any other cgroup — a nested systemd in a
// service, an init.scope below something else, another user's
// user@UID.service, an unreadable uid — names no subtree and pins
// nothing.
func TestManagerListenerUserManagerRoot(t *testing.T) {
	for _, tc := range []struct {
		name, cg string
		uid      uint32
	}{
		{"in a service", "/user.slice/user-1000.slice/user@1000.service/app.slice/nested.service", 1000},
		{"init.scope of a service", "/system.slice/evil.service/init.scope", 1000},
		{"another user's manager unit", "/user.slice/user-1001.slice/user@1001.service/init.scope", 1000},
		{"look-alike unit", "/user.slice/user-1000.slice/user@1000.service-evil/init.scope", 1000},
		{"no uid", "/user.slice/user-1000.slice/user@1000.service/init.scope", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeProc(t)
			f.listenTable()
			f.proc(900, 900, 1, 'S', 1, "/usr/lib/systemd/systemd")
			if tc.uid != 0 {
				f.uid(900, tc.uid)
			}
			f.cgroup(900, tc.cg)
			f.socket(900, 40, "111")
			if a, err := f.fs().ManagerListener(8080); err == nil {
				t.Fatalf("pinned %+v; want no known subtree", a)
			}
		})
	}
}

// TestActivatedHoldersNotUserManagerUnit (task lk4): pid 1's socket held
// by a process in a user manager's unit tree (a desktop app scope under
// user@UID.service) is not pid 1's activated service.
func TestActivatedHoldersNotUserManagerUnit(t *testing.T) {
	f := newFakeProc(t)
	f.listenTable()
	f.proc(1, 1, 0, 'S', 1, "/usr/lib/systemd/systemd")
	f.socket(1, 40, "111")
	fs := f.fs()
	a, err := fs.ManagerListener(8080)
	if err != nil {
		t.Fatalf("ManagerListener: %v", err)
	}
	f.proc(70, 70, 1, 'S', 2, "/usr/bin/app")
	f.cgroup(70, "/user.slice/user-1000.slice/user@1000.service/app.slice/app-x.scope")
	f.socket(70, 3, "111")
	adopt, refuse, err := fs.ActivatedHolders(a)
	if err != nil || len(adopt) != 0 || !slices.Equal(holderPIDs(refuse), []uint32{70}) {
		t.Fatalf("adopt %v refuse %v err %v; want refuse [70]", holderPIDs(adopt), holderPIDs(refuse), err)
	}
}

// TestManagerListenerRefuses (task lk4): pinning is only for a port the
// manager alone holds, through a cgroup that tells where its units run;
// a port an app holds, a port nothing holds and a user manager with no
// readable cgroup pin nothing.
func TestManagerListenerRefuses(t *testing.T) {
	f := newFakeProc(t)
	f.listenTable()
	fs := f.fs()
	if _, err := fs.ManagerListener(8080); err == nil {
		t.Fatal("no holder: pinned")
	}
	f.proc(900, 900, 1, 'S', 1, "/usr/lib/systemd/systemd")
	f.socket(900, 40, "111")
	if _, err := fs.ManagerListener(8080); err == nil {
		t.Fatal("user manager without a cgroup: pinned")
	}
	f.proc(50, 50, 1, 'S', 2, "/usr/bin/app")
	f.socket(50, 3, "111")
	if _, err := fs.ManagerListener(8080); err == nil {
		t.Fatal("an app holds the port: pinned")
	}
}

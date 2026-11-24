package target

import (
	"errors"
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
	adopt, refuse, err := fs.ActivatedHolders(a)
	if err != nil || !slices.Equal(holderPIDs(adopt), []uint32{50, 52}) ||
		!slices.Equal(holderPIDs(refuse), []uint32{51, 55, 56}) {
		t.Fatalf("adopt %v refuse %v err %v; want adopt [50 52], refuse [51 55 56]",
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

// TestActivatedHoldersUserManager (task lk4): a user manager's services
// run below its user@UID.service; a .service elsewhere holding its socket
// is not one of them.
func TestActivatedHoldersUserManager(t *testing.T) {
	f := newFakeProc(t)
	f.listenTable()
	um := "/user.slice/user-1000.slice/user@1000.service"
	f.proc(900, 900, 1, 'S', 1, "/usr/lib/systemd/systemd")
	f.cgroup(900, um+"/init.scope")
	f.socket(900, 40, "111")
	fs := f.fs()
	a, err := fs.ManagerListener(8080)
	if err != nil {
		t.Fatalf("ManagerListener: %v", err)
	}
	f.proc(60, 60, 900, 'S', 2, "/usr/bin/app")
	f.cgroup(60, um+"/app.slice/app.service")
	f.socket(60, 3, "111")
	f.proc(61, 61, 1, 'S', 2, "/usr/bin/app")
	f.cgroup(61, "/system.slice/app.service")
	f.socket(61, 3, "111")
	f.proc(62, 62, 1, 'S', 2, "/usr/bin/bash")
	f.cgroup(62, um+"/app.slice/app-term.scope")
	f.socket(62, 3, "111")
	adopt, refuse, err := fs.ActivatedHolders(a)
	if err != nil || !slices.Equal(holderPIDs(adopt), []uint32{60}) ||
		!slices.Equal(holderPIDs(refuse), []uint32{61, 62}) {
		t.Fatalf("adopt %v refuse %v err %v; want adopt [60], refuse [61 62]",
			holderPIDs(adopt), holderPIDs(refuse), err)
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

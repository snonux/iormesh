package internal

import (
	"encoding/binary"
	"net"
	"os"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"iormesh/internal/types"

	bpf "github.com/aquasecurity/libbpfgo"
)

// These tests need root: they load the real BPF object (the embedded one, or
// IORMESH_BPF_OBJECT), attach the connect/accept handlers and
// sock:inet_sock_set_state, and read the ring buffer. They skip for any other
// user, so `mage test` passes them by; `mage bpfRootTest` runs them as root
// and fails unless each is reported as passed (a skip is not a pass).
//
// They stand in for the M1 loader (step 5), which does not exist yet, so
// they drive libbpfgo directly and attach only what TCP tracing needs. What
// they prove: the whole object passes the verifier on this kernel; a Go
// net.Dial (a non-blocking connect returning EINPROGRESS, like Java NIO)
// from a PID in target_map yields the connection's 4-tuple from the
// SYN_SENT -> ESTABLISHED transition, attributed to the connecting fd
// although that transition fires in softirq; the accept side yields the
// mirrored tuple as a snapshot record; TCP_CLOSE unregisters both sockets;
// a connect failing before any state transition and an accept of a
// connection reset in the accept queue (plain accept(2), not accept4) leave
// no registration behind; and an empty target_map, or one naming another
// PID, yields nothing. What they do not prove: the
// loader's attach set, IPv6, or anything about Java processes.

// TCP states as the kernel numbers them (include/net/tcp_states.h).
const (
	tcpEstablished = 1
	tcpSynSent     = 2
	tcpClose       = 7
)

// sockTestPrograms are the programs a TCP tuple needs, by tracepoint.
var sockTestPrograms = map[string][2]string{
	"handle_sys_enter_connect":   {"syscalls", "sys_enter_connect"},
	"handle_sys_exit_connect":    {"syscalls", "sys_exit_connect"},
	"handle_sys_exit_accept":     {"syscalls", "sys_exit_accept"},
	"handle_sys_exit_accept4":    {"syscalls", "sys_exit_accept4"},
	"handle_inet_sock_set_state": {"sock", "inet_sock_set_state"},
}

// targetTraced is IORMESH_TARGET_TRACED, the state filter() traces (maps.h).
const targetTraced = 1

// targetValue mirrors struct iormesh_target (maps.h): 16 bytes, no padding.
// The map update copies the map's full value size from the pointer it is
// given, so the value written must be exactly this struct: a narrower Go
// value would have the kernel read the bytes after it - heap garbage that
// now and then made state something other than TRACED, and filter() then
// dropped every syscall of the test process (task ld4).
type targetValue struct {
	State uint32
	Gap   uint32
	Since uint64
}

// sockTracer is the loaded object with the ring buffer drained into events.
type sockTracer struct {
	module *bpf.Module
	events chan []byte
}

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root to load BPF programs (run `mage bpfRootTest`)")
	}
}

// newSockTracer loads the full object, attaches sockTestPrograms and starts
// polling event_map. Everything is released at test cleanup.
func newSockTracer(t *testing.T) *sockTracer {
	t.Helper()
	// libbpf logs every CO-RE relocation of ~1500 programs at debug level;
	// warnings (a verifier rejection among them) are all a failure needs.
	bpf.SetLoggerCbs(bpf.Callbacks{LogFilters: []func(int, string) bool{
		func(level int, _ string) bool { return level > bpf.LibbpfWarnLevel },
	}})
	module, step, err := LoadBPFModule()
	if err != nil {
		t.Fatalf("%s: %v", step, err)
	}
	t.Cleanup(module.Close)
	if err := module.BPFLoadObject(); err != nil {
		t.Fatalf("load BPF object: %v", err)
	}
	for prog, tp := range sockTestPrograms {
		p, err := module.GetProgram(prog)
		if err != nil {
			t.Fatalf("program %s: %v", prog, err)
		}
		if _, err := p.AttachTracepoint(tp[0], tp[1]); err != nil {
			t.Fatalf("attach %s to %s:%s: %v", prog, tp[0], tp[1], err)
		}
	}
	tr := &sockTracer{module: module, events: make(chan []byte, 4096)}
	rb, err := module.InitRingBuf("event_map", tr.events)
	if err != nil {
		t.Fatalf("ring buffer: %v", err)
	}
	rb.Poll(50)
	t.Cleanup(rb.Close)
	return tr
}

// setTarget inserts (on) or removes the tgid from target_map. An insert
// sets the tgid's target hint bit first, as internal/bpf's AddTarget does:
// filter() looks target_map up only behind a set bit (targethint.c).
func (tr *sockTracer) setTarget(t *testing.T, tgid uint32, on bool) {
	t.Helper()
	targets, err := tr.module.GetMap("target_map")
	if err != nil {
		t.Fatalf("target_map: %v", err)
	}
	if !on {
		if err := targets.DeleteKey(unsafe.Pointer(&tgid)); err != nil {
			t.Fatalf("remove %d from target_map: %v", tgid, err)
		}
		return
	}
	traced := targetValue{State: targetTraced}
	if size := int(unsafe.Sizeof(traced)); targets.ValueSize() != size {
		t.Fatalf("target_map value is %d bytes, targetValue %d", targets.ValueSize(), size)
	}
	tr.setHint(t, tgid)
	if err := targets.Update(unsafe.Pointer(&tgid), unsafe.Pointer(&traced)); err != nil {
		t.Fatalf("add %d to target_map: %v", tgid, err)
	}
}

// setHint sets tgid's bit in target_hint_user, the userspace half of the
// target hint, folded to its 1024 words (IORMESH_HINT_USER_WORDS).
func (tr *sockTracer) setHint(t *testing.T, tgid uint32) {
	t.Helper()
	hint, err := tr.module.GetMap("target_hint_user")
	if err != nil {
		t.Fatalf("target_hint_user: %v", err)
	}
	word := (tgid >> 6) % 1024
	raw, err := hint.GetValue(unsafe.Pointer(&word))
	if err != nil || len(raw) != 8 {
		t.Fatalf("read target hint word %d: %d bytes, %v", word, len(raw), err)
	}
	bits := *(*uint64)(unsafe.Pointer(&raw[0])) | 1<<(tgid&63)
	if err := hint.Update(unsafe.Pointer(&word), unsafe.Pointer(&bits)); err != nil {
		t.Fatalf("set target hint of %d: %v", tgid, err)
	}
}

// ownerRegistered reports whether sock_owner_map holds an entry for sk.
func (tr *sockTracer) ownerRegistered(t *testing.T, sk uint64) bool {
	t.Helper()
	owners, err := tr.module.GetMap("sock_owner_map")
	if err != nil {
		t.Fatalf("sock_owner_map: %v", err)
	}
	_, err = owners.GetValue(unsafe.Pointer(&sk))
	return err == nil
}

// entries returns the number of entries in the named map.
func (tr *sockTracer) entries(t *testing.T, name string) int {
	t.Helper()
	m, err := tr.module.GetMap(name)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	it := m.Iterator()
	n := 0
	for it.Next() {
		n++
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterate %s: %v", name, err)
	}
	return n
}

// waitTCPState returns the first tcp_state_event that match accepts, or nil
// after timeout. Records of other types (the attached syscall handlers'
// own) are skipped.
func (tr *sockTracer) waitTCPState(timeout time.Duration, match func(*types.TcpStateEvent) bool) *types.TcpStateEvent {
	deadline := time.After(timeout)
	for {
		select {
		case raw := <-tr.events:
			if len(raw) < 4 || binary.LittleEndian.Uint32(raw) != types.TCP_STATE_EVENT {
				continue
			}
			ev := types.NewTcpStateEvent(raw)
			if ev == nil {
				continue
			}
			if match(ev) {
				return ev
			}
		case <-deadline:
			return nil
		}
	}
}

// loopbackPair is one connection over 127.0.0.1, both ends in this process.
type loopbackPair struct {
	client, server         *net.TCPConn
	clientFd, serverFd     int32
	clientPort, serverPort uint32
}

// dialLoopback listens on an ephemeral port and connects to it. Go's
// net.Dial connects non-blocking and waits for writability: the connect
// syscall itself returns EINPROGRESS, as a Java NIO connect does.
func dialLoopback(t *testing.T) *loopbackPair {
	t.Helper()
	ln, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan *net.TCPConn, 1)
	go func() {
		c, err := ln.AcceptTCP()
		if err != nil {
			accepted <- nil
			return
		}
		accepted <- c
	}()
	client, err := net.DialTCP("tcp4", nil, ln.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	server := <-accepted
	if server == nil {
		t.Fatal("accept failed")
	}
	p := &loopbackPair{client: client, server: server,
		clientPort: uint32(client.LocalAddr().(*net.TCPAddr).Port),
		serverPort: uint32(ln.Addr().(*net.TCPAddr).Port)}
	p.clientFd, p.serverFd = connFd(t, client), connFd(t, server)
	return p
}

// connFd returns the descriptor number behind c.
func connFd(t *testing.T, c *net.TCPConn) int32 {
	t.Helper()
	raw, err := c.SyscallConn()
	if err != nil {
		t.Fatalf("syscall conn: %v", err)
	}
	var fd int32
	if err := raw.Control(func(f uintptr) { fd = int32(f) }); err != nil {
		t.Fatalf("control: %v", err)
	}
	return fd
}

func (p *loopbackPair) close() {
	_ = p.client.Close()
	_ = p.server.Close()
}

// isLoopbackV4 reports whether ev's addresses are both 127.0.0.1 over IPv4,
// with the unused 12 bytes zeroed (the record is zeroed before filling).
func isLoopbackV4(ev *types.TcpStateEvent) bool {
	want := [16]byte{127, 0, 0, 1}
	return ev.Family == syscall.AF_INET && ev.Saddr == want && ev.Daddr == want
}

// TestSockLoopbackConnectTuple: both ends of a loopback connection of a
// target PID are reported with their 4-tuple, owner pid and fd, and both
// sockets are unregistered once they reach TCP_CLOSE.
func TestSockLoopbackConnectTuple(t *testing.T) {
	requireRoot(t)
	tr := newSockTracer(t)
	pid := uint32(os.Getpid())
	tr.setTarget(t, pid, true)

	p := dialLoopback(t)
	client := tr.waitTCPState(5*time.Second, func(ev *types.TcpStateEvent) bool {
		return ev.Oldstate == tcpSynSent && ev.Newstate == tcpEstablished &&
			ev.Sport == p.clientPort && ev.Dport == p.serverPort
	})
	if client == nil {
		t.Fatalf("no SYN_SENT -> ESTABLISHED record for 127.0.0.1:%d -> :%d", p.clientPort, p.serverPort)
	}
	checkOwner(t, "client", client, pid, p.clientFd)
	server := tr.waitTCPState(5*time.Second, func(ev *types.TcpStateEvent) bool {
		return ev.Oldstate == types.TCP_STATE_SNAPSHOT && ev.Sport == p.serverPort && ev.Dport == p.clientPort
	})
	if server == nil {
		t.Fatalf("no accept snapshot for 127.0.0.1:%d <- :%d", p.serverPort, p.clientPort)
	}
	checkOwner(t, "server", server, pid, p.serverFd)
	if server.Newstate != tcpEstablished {
		t.Errorf("accept snapshot state = %d, want ESTABLISHED", server.Newstate)
	}
	if client.Sk == server.Sk {
		t.Errorf("both ends report the same socket %#x", client.Sk)
	}
	checkClosed(t, tr, p, client.Sk, server.Sk)
}

// checkOwner checks that ev is attributed to pid/fd and carries a loopback
// IPv4 tuple.
func checkOwner(t *testing.T, side string, ev *types.TcpStateEvent, pid uint32, fd int32) {
	t.Helper()
	if ev.Pid != pid || ev.Fd != fd {
		t.Errorf("%s: owner pid=%d fd=%d, want pid=%d fd=%d", side, ev.Pid, ev.Fd, pid, fd)
	}
	if !isLoopbackV4(ev) {
		t.Errorf("%s: family=%d saddr=%v daddr=%v, want 127.0.0.1 both ends", side, ev.Family, ev.Saddr, ev.Daddr)
	}
	if ev.SchemaVersion != types.TCP_STATE_EVENT_SCHEMA_VERSION || ev.Sk == 0 {
		t.Errorf("%s: schema=%d sk=%#x", side, ev.SchemaVersion, ev.Sk)
	}
}

// checkClosed closes both ends and waits for each socket's TCP_CLOSE record,
// after which sock_owner_map must have forgotten it.
func checkClosed(t *testing.T, tr *sockTracer, p *loopbackPair, sks ...uint64) {
	t.Helper()
	p.close()
	pending := map[uint64]bool{}
	for _, sk := range sks {
		pending[sk] = true
	}
	tr.waitTCPState(5*time.Second, func(ev *types.TcpStateEvent) bool {
		if ev.Newstate == tcpClose {
			delete(pending, ev.Sk)
		}
		return len(pending) == 0
	})
	for sk := range pending {
		t.Errorf("no TCP_CLOSE record for socket %#x", sk)
	}
	for _, sk := range sks {
		if tr.ownerRegistered(t, sk) {
			t.Errorf("socket %#x still in sock_owner_map after TCP_CLOSE", sk)
		}
	}
}

// TestSockNonTargetPidNoEvents: with target_map empty, and then naming
// another PID, a loopback connection of this process yields no tcp_state
// record at all.
// A second connection after adding this PID is the positive control: it
// shows the same tracer would have reported the first one.
func TestSockNonTargetPidNoEvents(t *testing.T) {
	requireRoot(t)
	tr := newSockTracer(t)
	pid := uint32(os.Getpid())
	expectNoRecords(t, tr, pid, "empty target_map")
	tr.setTarget(t, uint32(os.Getppid()), true)
	expectNoRecords(t, tr, pid, "target_map naming another PID")

	tr.setTarget(t, pid, true)
	p := dialLoopback(t)
	defer p.close()
	if tr.waitTCPState(5*time.Second, func(ev *types.TcpStateEvent) bool {
		return ev.Pid == pid && ev.Sport == p.clientPort && ev.Newstate == tcpEstablished
	}) == nil {
		t.Fatal("positive control: no record once this PID is a target")
	}
}

// expectNoRecords makes a loopback connection that must yield no
// tcp_state record and no socket registration.
func expectNoRecords(t *testing.T, tr *sockTracer, pid uint32, phase string) {
	t.Helper()
	ignored := dialLoopback(t)
	ignored.close()
	stray := tr.waitTCPState(time.Second, func(ev *types.TcpStateEvent) bool {
		return ev.Pid == pid || ev.Sport == ignored.clientPort || ev.Sport == ignored.serverPort
	})
	if stray != nil {
		t.Fatalf("%s: non-target PID reported: %v", phase, stray)
	}
	if n := tr.entries(t, "sock_owner_map"); n != 0 {
		t.Fatalf("%s: sock_owner_map holds %d entries, want 0", phase, n)
	}
}

// TestSockFailedConnectUnregisters: a connect that fails before the kernel
// sets SYN_SENT (ENETUNREACH for the broadcast address) has no state
// transition, and neither has the close that follows; the connect exit
// must drop the registration, or a later socket reusing the freed struct
// sock's address would be attributed to this pid/fd.
func TestSockFailedConnectUnregisters(t *testing.T) {
	requireRoot(t)
	tr := newSockTracer(t)
	tr.setTarget(t, uint32(os.Getpid()), true)

	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socket: %v", err)
	}
	err = syscall.Connect(fd, &syscall.SockaddrInet4{Port: 9, Addr: [4]byte{255, 255, 255, 255}})
	if err != syscall.ENETUNREACH {
		_ = syscall.Close(fd)
		t.Fatalf("connect to 255.255.255.255 = %v, want ENETUNREACH (the early-failure path)", err)
	}
	if n := tr.entries(t, "sock_owner_map"); n != 0 {
		t.Errorf("after the failed connect sock_owner_map holds %d entries, want 0", n)
	}
	if err := syscall.Close(fd); err != nil {
		t.Fatalf("close: %v", err)
	}
	if n := tr.entries(t, "sock_owner_map"); n != 0 {
		t.Errorf("after close sock_owner_map holds %d entries, want 0", n)
	}
	if n := tr.entries(t, "connect_sock_map"); n != 0 {
		t.Errorf("connect_sock_map holds %d entries after the exit, want 0", n)
	}
}

// TestSockResetBeforeAcceptNotRegistered: a connection the peer resets while
// it waits in the accept queue is TCP_CLOSE when accept(2) returns it and
// never transitions again. Its snapshot is still reported (state
// TCP_CLOSE), but it must not be registered. The accept is the raw
// accept(2) syscall, so sys_exit_accept is exercised (Go's own accepts use
// accept4).
func TestSockResetBeforeAcceptNotRegistered(t *testing.T) {
	requireRoot(t)
	tr := newSockTracer(t)
	tr.setTarget(t, uint32(os.Getpid()), true)
	lfd, port := rawListener(t)

	client, err := net.DialTCP("tcp4", nil, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(port)})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	clientPort := uint32(client.LocalAddr().(*net.TCPAddr).Port)
	_ = client.SetLinger(0) // close sends RST instead of FIN
	_ = client.Close()
	time.Sleep(100 * time.Millisecond) // let the RST reach the queued child

	nfd, _, errno := syscall.Syscall(syscall.SYS_ACCEPT, uintptr(lfd), 0, 0)
	if errno != 0 {
		t.Fatalf("accept: %v", errno)
	}
	defer func() { _ = syscall.Close(int(nfd)) }()
	snap := tr.waitTCPState(5*time.Second, func(ev *types.TcpStateEvent) bool {
		return ev.Oldstate == types.TCP_STATE_SNAPSHOT && ev.Dport == clientPort
	})
	if snap == nil {
		t.Fatal("no accept snapshot from accept(2)")
	}
	if snap.Newstate != tcpClose || snap.Fd != int32(nfd) {
		t.Errorf("snapshot state=%d fd=%d, want TCP_CLOSE fd=%d", snap.Newstate, snap.Fd, nfd)
	}
	if n := tr.entries(t, "sock_owner_map"); n != 0 {
		t.Errorf("sock_owner_map holds %d entries, want 0 (reset child registered?)", n)
	}
}

// rawListener returns a listening IPv4 loopback socket made with raw
// syscalls (closed at cleanup) and its port.
func rawListener(t *testing.T) (int, uint32) {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socket: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := syscall.Listen(fd, 8); err != nil {
		t.Fatalf("listen: %v", err)
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		t.Fatalf("getsockname: %v", err)
	}
	return fd, uint32(sa.(*syscall.SockaddrInet4).Port)
}

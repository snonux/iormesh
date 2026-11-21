package internal

import (
	"net"
	"os"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"iormesh/internal/types"
)

// A sock_owner_map entry that outlives its socket (task if4): the kernel
// skips a tracepoint run whenever another tracing BPF program already runs
// on that CPU, so the TCP_CLOSE record that would have dropped the entry
// can be lost, and the freed struct sock's address then comes back as
// another socket - on the Rocky 9.9 M4 hosts another process's, whose
// whole life was recorded under the dead socket's pid, fd and conn id.
// These tests plant exactly the entry such a lost run leaves behind and
// check that the tracer recognizes it as belonging to an earlier life of
// the struct sock (sock.c, iormesh_sock_owner_stale): no record goes out
// under the stale owner, and the entry is dropped. Root only, like
// sock_root_test.go, whose tracer they reuse.

// sockOwnerValue mirrors struct sock_owner (sock.c): 16 bytes, no padding.
type sockOwnerValue struct {
	Pid uint32
	Fd  int32
	Ino uint64
}

// plantOwner writes owner as sk's sock_owner_map entry, as a registration
// whose TCP_CLOSE run the kernel skipped would have left it.
func (tr *sockTracer) plantOwner(t *testing.T, sk uint64, owner sockOwnerValue) {
	t.Helper()
	owners, err := tr.module.GetMap("sock_owner_map")
	if err != nil {
		t.Fatalf("sock_owner_map: %v", err)
	}
	if size := int(unsafe.Sizeof(owner)); owners.ValueSize() != size {
		t.Fatalf("sock_owner_map value is %d bytes, sockOwnerValue %d", owners.ValueSize(), size)
	}
	if err := owners.Update(unsafe.Pointer(&sk), unsafe.Pointer(&owner)); err != nil {
		t.Fatalf("plant owner of %#x: %v", sk, err)
	}
}

// socketIno returns the inode number of the socket file behind c - what
// the registration stores as the owner's generation.
func socketIno(t *testing.T, c *net.TCPConn) uint64 {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Fstat(int(connFd(t, c)), &st); err != nil {
		t.Fatalf("fstat: %v", err)
	}
	return st.Ino
}

// drainTCPStates returns the tcp_state records that arrive within quiet of
// each other (the first wait counts too).
func (tr *sockTracer) drainTCPStates(quiet time.Duration) []*types.TcpStateEvent {
	var got []*types.TcpStateEvent
	for {
		ev := tr.waitTCPState(quiet, func(*types.TcpStateEvent) bool { return true })
		if ev == nil {
			return got
		}
		got = append(got, ev)
	}
}

// waitOwnersGone waits, sleeping between looks, until sock_owner_map is
// empty, and fails the test after timeout.
func (tr *sockTracer) waitOwnersGone(t *testing.T, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for tr.entries(t, "sock_owner_map") != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("sock_owner_map holds %d entries after the sockets closed",
				tr.entries(t, "sock_owner_map"))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// reuseSockets is how many target connections TestSockReuseAfterLostClose
// leaves stale entries for, and reuseChurn how many non-target connections
// it then makes to have the slab hand their struct socks out again.
const (
	reuseSockets = 64
	reuseChurn   = 1500
)

// TestSockReuseAfterLostClose: the target's sockets close, their entries
// are planted back as if every TCP_CLOSE run had been skipped, and this
// process - no longer a target - churns loopback connections until the
// slab reuses some of the freed struct socks for them (client, accepted
// child, listener). None of the churn may be recorded, and every reused
// socket's stale entry must be gone. Without the generation check each
// reused socket's life was recorded under the dead socket's owner, the
// M4-9 failure of task lb4.
func TestSockReuseAfterLostClose(t *testing.T) {
	requireRoot(t)
	tr := newSockTracer(t)
	pid := uint32(os.Getpid())
	tr.setTarget(t, pid, true)

	stale := targetSockets(t, tr, pid)
	tr.waitOwnersGone(t, 5*time.Second)
	tr.setTarget(t, pid, false)
	tr.drainTCPStates(200 * time.Millisecond)
	for sk, owner := range stale {
		tr.plantOwner(t, sk, owner)
	}

	churnLoopback(t, reuseChurn)
	if got := tr.drainTCPStates(500 * time.Millisecond); len(got) != 0 {
		ev := got[0]
		t.Errorf("%d records of non-target sockets under a stale owner, first: sk=%#x pid=%d fd=%d %d->%d port %d->%d",
			len(got), ev.Sk, ev.Pid, ev.Fd, ev.Oldstate, ev.Newstate, ev.Sport, ev.Dport)
	}
	reused := 0
	for sk := range stale {
		if !tr.ownerRegistered(t, sk) {
			reused++
		}
	}
	if reused == 0 {
		t.Fatalf("none of %d freed struct socks was reused by %d connections: the test proved nothing",
			len(stale), reuseChurn)
	}
	t.Logf("%d of %d stale entries met a reused struct sock and were dropped", reused, len(stale))
}

// targetSockets makes reuseSockets loopback connections as a target, closes
// them, and returns each socket's owner as registered: pid, fd and socket
// inode, keyed by struct sock address taken from its records.
func targetSockets(t *testing.T, tr *sockTracer, pid uint32) map[uint64]sockOwnerValue {
	t.Helper()
	inoOfFd := map[int32]uint64{}
	var pairs []*loopbackPair
	for range reuseSockets / 2 {
		p := dialLoopback(t)
		inoOfFd[p.clientFd] = socketIno(t, p.client)
		inoOfFd[p.serverFd] = socketIno(t, p.server)
		pairs = append(pairs, p)
	}
	owners := map[uint64]sockOwnerValue{}
	tr.waitTCPState(5*time.Second, func(ev *types.TcpStateEvent) bool {
		if ino, ok := inoOfFd[ev.Fd]; ok && ev.Pid == pid {
			owners[ev.Sk] = sockOwnerValue{Pid: pid, Fd: ev.Fd, Ino: ino}
		}
		return len(owners) == reuseSockets
	})
	if len(owners) != reuseSockets {
		t.Fatalf("records name %d sockets, want %d", len(owners), reuseSockets)
	}
	for _, p := range pairs {
		p.close()
	}
	return owners
}

// churnLoopback makes n short loopback connections, one at a time, each
// on a fresh listener so listeners are churned too.
func churnLoopback(t *testing.T, n int) {
	t.Helper()
	for range n {
		dialLoopback(t).close()
	}
}

// TestSockStaleOwnerOfLiveSocket: an entry naming another socket file's
// generation is stale even when its socket shows no new-life transition
// (the CLOSE -> SYN_SENT or LISTEN -> SYN_RECV run lost as well). The
// client's close then reaches FIN_WAIT1 with its socket file still
// attached, whose inode differs from the planted owner's: nothing is
// recorded, and the entry is dropped.
func TestSockStaleOwnerOfLiveSocket(t *testing.T) {
	requireRoot(t)
	tr := newSockTracer(t)
	pid := uint32(os.Getpid())
	tr.setTarget(t, pid, true)

	p := dialLoopback(t)
	defer p.close()
	client := tr.waitTCPState(5*time.Second, func(ev *types.TcpStateEvent) bool {
		return ev.Newstate == tcpEstablished && ev.Sport == p.clientPort && ev.Dport == p.serverPort
	})
	if client == nil {
		t.Fatalf("no ESTABLISHED record for 127.0.0.1:%d -> :%d", p.clientPort, p.serverPort)
	}
	tr.setTarget(t, pid, false)
	tr.drainTCPStates(200 * time.Millisecond)
	ghost := sockOwnerValue{Pid: 1, Fd: 999, Ino: socketIno(t, p.client) + 1}
	tr.plantOwner(t, client.Sk, ghost)

	_ = p.client.Close()
	for _, ev := range tr.drainTCPStates(500 * time.Millisecond) {
		if ev.Sk == client.Sk {
			t.Errorf("record under the stale owner: pid=%d fd=%d %d->%d", ev.Pid, ev.Fd, ev.Oldstate, ev.Newstate)
		}
	}
	if tr.ownerRegistered(t, client.Sk) {
		t.Errorf("stale entry of %#x still in sock_owner_map after its FIN_WAIT1", client.Sk)
	}
}

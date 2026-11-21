package internal

import (
	"bytes"
	"encoding/binary"
	"os"
	"syscall"
	"testing"
	"time"
	"unsafe"

	bpf "github.com/aquasecurity/libbpfgo"

	"iormesh/internal/types"
)

// The end-of-session counter snapshot (sock.c, iormesh_sock_final; task
// sf4), read from the real kernel: a loopback connection of this process
// is still open when the "session" ends, and running the program for
// each of its two registered sockets — as bpf.Tracer.FinalSockets does,
// through BPF_PROG_TEST_RUN, nothing attached — yields a final record with
// the socket's owner, tuple and the counters the exchange implies. Root
// only; it reuses sock_root_test.go's tracer. internal/bpf cannot be
// imported here (it imports this package), so the run is driven directly.

// TestSockFinalSnapshot: an open connection's two ends each give one final
// snapshot — TCP_STATE_FINAL, ESTABLISHED, owner pid and fd, the mirrored
// tuple — whose counters count the exchange (the client's own SYN acked
// too) and whose fields rebuild one ISN pair on both ends. The program's
// two guards each refuse a planted owner entry: one naming the live
// client socket with another inode (a stale entry whose address a new
// socket took), and one naming a socket a refused connect left in
// TCP_CLOSE, still held by its fd. Once both sockets closed, neither gives
// one, nor does an address nobody registered.
func TestSockFinalSnapshot(t *testing.T) {
	requireRoot(t)
	tr := newSockTracer(t)
	tr.setTarget(t, uint32(os.Getpid()), true)

	p := dialLoopback(t)
	exchange(t, p)
	client, server := tr.finalPair(t, p)
	for _, w := range []struct {
		side            string
		ev              *types.TcpStateEvent
		fd              int32
		sport, dport    uint32
		acked, received uint64
	}{
		{"client", client, p.clientFd, p.clientPort, p.serverPort, 1 + seqRequest, seqResponse},
		{"server", server, p.serverFd, p.serverPort, p.clientPort, seqResponse, seqRequest},
	} {
		ev := w.ev
		if ev.Oldstate != types.TCP_STATE_FINAL || ev.Newstate != tcpEstablished ||
			ev.SeqStatus != types.TCP_SEQ_OK || ev.SchemaVersion != types.TCP_STATE_EVENT_SCHEMA_VERSION {
			t.Errorf("%s: old %d new %d seq status %d schema %d", w.side, ev.Oldstate, ev.Newstate,
				ev.SeqStatus, ev.SchemaVersion)
		}
		if ev.Pid != uint32(os.Getpid()) || ev.Fd != w.fd || ev.Sport != w.sport || ev.Dport != w.dport ||
			!isLoopbackV4(ev) {
			t.Errorf("%s: pid %d fd %d ports %d -> %d, want %d fd %d ports %d -> %d", w.side,
				ev.Pid, ev.Fd, ev.Sport, ev.Dport, os.Getpid(), w.fd, w.sport, w.dport)
		}
		if ev.BytesAcked != w.acked || ev.BytesReceived != w.received {
			t.Errorf("%s: bytes_acked %d bytes_received %d, want %d and %d", w.side,
				ev.BytesAcked, ev.BytesReceived, w.acked, w.received)
		}
	}
	if c, s := rebuild(client, false), rebuild(server, true); c != s {
		t.Errorf("the final snapshots rebuild %+v (client) and %+v (server)", c, s)
	}

	tr.checkFinalGuards(t, p, client.Sk)

	p.close()
	tr.collectUntilClosed(t, p)
	for _, sk := range []uint64{client.Sk, server.Sk, 1} {
		if ev, ok := tr.finalSnapshot(t, sk); ok {
			t.Errorf("socket %#x gave a final snapshot after it closed: %+v", sk, ev)
		}
	}
}

// checkFinalGuards plants the two owner entries iormesh_sock_final must
// refuse and requires no record for either. clientSk is p's live client
// socket; its real entry is put back afterwards.
func (tr *sockTracer) checkFinalGuards(t *testing.T, p *loopbackPair, clientSk uint64) {
	t.Helper()
	pid := uint32(os.Getpid())
	ino := socketIno(t, p.client)
	tr.plantOwner(t, clientSk, sockOwnerValue{Pid: pid, Fd: p.clientFd, Ino: ino + 1})
	if ev, ok := tr.finalSnapshot(t, clientSk); ok {
		t.Errorf("an owner entry of another inode gave a final snapshot: %+v", ev)
	}
	tr.plantOwner(t, clientSk, sockOwnerValue{Pid: pid, Fd: p.clientFd, Ino: ino})

	fd, sk := tr.refusedSocket(t)
	defer func() { _ = syscall.Close(fd) }()
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		t.Fatalf("fstat: %v", err)
	}
	tr.plantOwner(t, sk, sockOwnerValue{Pid: pid, Fd: int32(fd), Ino: st.Ino})
	if ev, ok := tr.finalSnapshot(t, sk); ok {
		t.Errorf("a socket in TCP_CLOSE gave a final snapshot: %+v", ev)
	}
}

// refusedSocket connects a new socket to a closed loopback port and
// returns its fd, still open, and its struct sock address, read from its
// SYN_SENT -> CLOSE record: a socket in TCP_CLOSE that its fd still holds.
func (tr *sockTracer) refusedSocket(t *testing.T) (int, uint64) {
	t.Helper()
	port := closedPort(t)
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socket: %v", err)
	}
	err = syscall.Connect(fd, &syscall.SockaddrInet4{Port: int(port), Addr: [4]byte{127, 0, 0, 1}})
	if err != syscall.ECONNREFUSED {
		_ = syscall.Close(fd)
		t.Fatalf("connect to closed port %d = %v, want ECONNREFUSED", port, err)
	}
	ev := tr.waitTCPState(5*time.Second, func(ev *types.TcpStateEvent) bool {
		return ev.Dport == port && ev.Newstate == tcpClose
	})
	if ev == nil {
		_ = syscall.Close(fd)
		t.Fatalf("no SYN_SENT -> CLOSE record of the refused connect to port %d", port)
	}
	return fd, ev.Sk
}

// finalPair waits until the final snapshots of p's two ends show the
// exchange acknowledged both ways — the last ACK may still be on its way
// when the reads returned — and returns them, client first. It polls a
// few times a second, bounded.
func (tr *sockTracer) finalPair(t *testing.T, p *loopbackPair) (client, server *types.TcpStateEvent) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		client, server = tr.finalOf(t, p)
		done := client != nil && server != nil &&
			client.BytesAcked == 1+seqRequest && server.BytesAcked == seqResponse
		if done || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if client == nil || server == nil {
		t.Fatalf("final snapshots: client %+v, server %+v; want both ends", client, server)
	}
	return client, server
}

// finalOf runs the snapshot for every registered socket, as
// FinalSockets walks sock_owner_map, and picks p's two ends by port.
func (tr *sockTracer) finalOf(t *testing.T, p *loopbackPair) (client, server *types.TcpStateEvent) {
	t.Helper()
	owners, err := tr.module.GetMap("sock_owner_map")
	if err != nil {
		t.Fatalf("sock_owner_map: %v", err)
	}
	var keys []uint64
	it := owners.Iterator()
	for it.Next() {
		keys = append(keys, binary.NativeEndian.Uint64(it.Key()))
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterate sock_owner_map: %v", err)
	}
	for _, sk := range keys {
		ev, ok := tr.finalSnapshot(t, sk)
		switch {
		case !ok:
		case ev.Sport == p.clientPort && ev.Dport == p.serverPort:
			client = ev
		case ev.Sport == p.serverPort && ev.Dport == p.clientPort:
			server = ev
		}
	}
	return client, server
}

// finalSnapshot runs iormesh_sock_final for sk and returns its record, or
// false when the program declined.
func (tr *sockTracer) finalSnapshot(t *testing.T, sk uint64) (*types.TcpStateEvent, bool) {
	t.Helper()
	prog, err := tr.module.GetProgram("iormesh_sock_final")
	if err != nil {
		t.Fatalf("iormesh_sock_final: %v", err)
	}
	ctx := binary.NativeEndian.AppendUint64(nil, sk)
	opts := bpf.RunOpts{CtxIn: ctx, CtxSizeIn: uint32(len(ctx))}
	if err := prog.Run(&opts); err != nil {
		t.Fatalf("test run of iormesh_sock_final: %v", err)
	}
	if opts.RetVal != 1 {
		return nil, false
	}
	out, err := tr.module.GetMap("sock_final_map")
	if err != nil {
		t.Fatalf("sock_final_map: %v", err)
	}
	zero := uint32(0)
	raw, err := out.GetValue(unsafe.Pointer(&zero))
	if err != nil {
		t.Fatalf("read sock_final_map: %v", err)
	}
	ev := &types.TcpStateEvent{}
	if err := binary.Read(bytes.NewReader(raw), binary.NativeEndian, ev); err != nil {
		t.Fatalf("decode the final snapshot: %v", err)
	}
	return ev, true
}

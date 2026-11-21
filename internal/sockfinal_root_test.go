package internal

import (
	"bytes"
	"encoding/binary"
	"os"
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
// too) and whose fields rebuild one ISN pair on both ends. Once both
// sockets closed, neither gives one, nor does an address nobody
// registered.
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

	p.close()
	tr.collectUntilClosed(t, p)
	for _, sk := range []uint64{client.Sk, server.Sk, 1} {
		if ev, ok := tr.finalSnapshot(t, sk); ok {
			t.Errorf("socket %#x gave a final snapshot after it closed: %+v", sk, ev)
		}
	}
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

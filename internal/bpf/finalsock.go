package bpf

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"unsafe"

	bpf "github.com/aquasecurity/libbpfgo"

	"iormesh/internal/types"
)

// The end-of-session counter snapshot (internal/c/sock.c,
// iormesh_sock_final; task sf4): the program userspace runs, never
// attaches, once per registered socket, the map it writes its record to,
// and the owners it walks.
const (
	sockFinalProg    = "iormesh_sock_final"
	sockFinalMapName = "sock_final_map"
	sockOwnerMapName = "sock_owner_map"
)

// FinalSockets takes the final snapshot of every TCP socket the session
// still has registered: one tcp_state record each (oldstate
// TCP_STATE_FINAL) with the socket's state, 4-tuple and tcp_sock counters
// at this moment, owned like its state changes. A socket closed during
// the session has none — its CLOSE record was its last — nor has a stale
// registration the program sees through (sock.c). The records are new
// values, not the ring's pooled ones; hand them to the correlator like
// any other, before Close (the maps die with the tracer) and after the
// records in flight were handled, so that each snapshot follows every
// call it can vouch for.
//
// Its counters are what makes the merge's counter proof complete
// (internal/federate/clockproof.go): every socket alive at the end gets a
// counter reading after its last recorded call, as a closed one has its
// CLOSE record. A failed run ends the walk with the records so far and
// the error: they are no loss of the app's records, only proofs the merge
// will not have.
func (t *Tracer) FinalSockets() ([]*types.TcpStateEvent, error) {
	if t.closed() {
		return nil, errClosed
	}
	owners, err := t.module.GetMap(sockOwnerMapName)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", sockOwnerMapName, err)
	}
	out, err := t.module.GetMap(sockFinalMapName)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", sockFinalMapName, err)
	}
	prog, err := t.module.GetProgram(sockFinalProg)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", sockFinalProg, err)
	}
	snap := func(sk uint64) ([]byte, error) {
		ctx := binary.NativeEndian.AppendUint64(nil, sk)
		// A raw tracepoint's test run takes its arguments as the context
		// and nothing else: no data, no repeat.
		opts := bpf.RunOpts{CtxIn: ctx, CtxSizeIn: uint32(len(ctx))}
		if err := prog.Run(&opts); err != nil {
			return nil, err
		}
		if opts.RetVal != 1 {
			return nil, nil
		}
		zero := uint32(0)
		return out.GetValue(unsafe.Pointer(&zero))
	}
	keys, err := ownerKeys(owners.Iterator(), owners.MaxEntries())
	if err != nil {
		return nil, fmt.Errorf("iterate %s: %w", sockOwnerMapName, err)
	}
	return finalRecords(keys, snap)
}

// keyIterator is what ownerKeys needs of a libbpfgo map iterator.
type keyIterator interface {
	Next() bool
	Key() []byte
	Err() error
}

// ownerKeys collects the socket addresses of sock_owner_map, each once.
// The targets still run while it walks: when the key the walk stands on
// is deleted meanwhile (a socket closing), the kernel's next-key lookup
// starts over from the first key, so keys come again — the set drops the
// repeats — and a map churning fast enough could restart it forever, so
// the walk stops after 4 × max entries steps, with what it has.
func ownerKeys(it keyIterator, maxEntries uint32) ([]uint64, error) {
	seen := map[uint64]bool{}
	var keys []uint64
	for steps := uint64(0); steps < 4*uint64(maxEntries) && it.Next(); steps++ {
		k := binary.NativeEndian.Uint64(it.Key())
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	return keys, it.Err()
}

// finalRecords runs snap for each socket address and decodes the records
// it returns (nil: no snapshot of that socket). Keys are collected before
// the first run, so the walk never iterates a map a run could change.
func finalRecords(keys []uint64, snap func(sk uint64) ([]byte, error)) ([]*types.TcpStateEvent, error) {
	var recs []*types.TcpStateEvent
	for _, sk := range keys {
		raw, err := snap(sk)
		if err != nil {
			return recs, fmt.Errorf("final snapshot of socket %#x: %w", sk, err)
		}
		if raw == nil {
			continue
		}
		ev, err := decodeFinal(raw)
		if err != nil {
			return recs, err
		}
		recs = append(recs, ev)
	}
	return recs, nil
}

// errFinalLayout: the snapshot slot holds no tcp_state_event of this
// build's layout, or not a final one.
var errFinalLayout = errors.New("bpf: final snapshot of an unknown layout")

// decodeFinal decodes one record of sock_final_map into a fresh value.
func decodeFinal(raw []byte) (*types.TcpStateEvent, error) {
	ev := &types.TcpStateEvent{}
	if len(raw) != binary.Size(ev) {
		return nil, fmt.Errorf("%w: %d bytes", errFinalLayout, len(raw))
	}
	if err := binary.Read(bytes.NewReader(raw), binary.NativeEndian, ev); err != nil {
		return nil, fmt.Errorf("%w: %w", errFinalLayout, err)
	}
	if ev.EventType != types.TCP_STATE_EVENT || ev.Oldstate != types.TCP_STATE_FINAL {
		return nil, fmt.Errorf("%w: type %d, old state %d", errFinalLayout, ev.EventType, ev.Oldstate)
	}
	return ev, nil
}

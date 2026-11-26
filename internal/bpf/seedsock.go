package bpf

import (
	"errors"
	"fmt"
	"io"
	"syscall"
	"unsafe"

	bpf "github.com/aquasecurity/libbpfgo"
)

// The session-start seeding (internal/c/sock.c, iormesh_seed_listen and
// iormesh_seed_sock; decision #35, task cm4): the two task-file iterators
// and the map that carries the first one's listeners to the second.
const (
	seedListenProg    = "iormesh_seed_listen"
	seedSockProg      = "iormesh_seed_sock"
	seedListenMapName = "seed_listen_map"
)

// SeedSockets registers every TCP socket the traced processes already
// hold — opened before the session, or before a process became a target —
// and has the kernel emit one initial snapshot of each into the ring
// buffer (a tcp_state record with oldstate TCP_STATE_INITIAL_CLIENT or
// _SERVER), so its reads and writes name a connection from here on. A
// socket registered already (a connect or accept of the session, an
// earlier seeding) is left alone, so calling it again after adding a
// target seeds only what is new. Call it after the probes attached and
// the targets are in target_map: a socket seeded before the state
// tracepoint ran could close unseen.
//
// It empties seed_listen_map, runs the listener pass, then the socket
// pass: each iterator is attached, read to the end once — the programs
// write nothing to the reader, their work is the visit — and detached.
// It empties the map again afterwards, so the addresses and ports of
// listeners that belong to no target do not stay in the kernel until
// the next seeding.
//
// The records go into the ring buffer while the caller may not be
// draining it (session.start seeds before its loop runs): the 16 MiB
// ring holds about 140,000 of them. Past that a record is dropped and
// counted like any ring drop; its socket stays registered, so its later
// state changes still arrive, but its reads and writes name no
// connection until one of them does.
func (t *Tracer) SeedSockets() (err error) {
	if t.closed() {
		return errClosed
	}
	t.seedMu.Lock()
	defer t.seedMu.Unlock()
	listeners, err := t.module.GetMap(seedListenMapName)
	if err != nil {
		return fmt.Errorf("get %s: %w", seedListenMapName, err)
	}
	if err := clearMap(listeners); err != nil {
		return fmt.Errorf("empty %s: %w", seedListenMapName, err)
	}
	defer func() {
		if cerr := clearMap(listeners); cerr != nil {
			err = errors.Join(err, fmt.Errorf("empty %s: %w", seedListenMapName, cerr))
		}
	}()
	for _, name := range []string{seedListenProg, seedSockProg} {
		if err := t.runIter(name); err != nil {
			return err
		}
	}
	return nil
}

// runIter attaches the task-file iterator name, reads it to the end and
// detaches it.
func (t *Tracer) runIter(name string) (err error) {
	prog, err := t.module.GetProgram(name)
	if err != nil {
		return fmt.Errorf("get %s: %w", name, err)
	}
	link, err := prog.AttachIter(bpf.IterOpts{})
	if err != nil {
		return fmt.Errorf("attach %s: %w", name, err)
	}
	defer func() {
		if derr := link.Destroy(); derr != nil {
			err = errors.Join(err, fmt.Errorf("detach %s: %w", name, derr))
		}
	}()
	r, err := link.Reader()
	if err != nil {
		return fmt.Errorf("open %s: %w", name, err)
	}
	defer func() { err = errors.Join(err, r.Close()) }()
	if err := drainIter(r); err != nil {
		return fmt.Errorf("run %s: %w", name, err)
	}
	return nil
}

// drainIter reads r until EOF. An iterator's read returns 0 at the end.
// EINTR retries, and so does EAGAIN: the kernel's bpf_seq_read
// (kernel/bpf/bpf_iter.c, since 5.8, so on el9) returns it when one
// read() visited MAX_ITER_OBJECTS (1M) objects without producing output
// — the seed programs never write any — and the walk resumes where it
// stopped on the next read. Ending there would leave every host with a
// million open files in view unseeded.
func drainIter(r io.Reader) error {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		switch {
		case errors.Is(err, syscall.EINTR), errors.Is(err, syscall.EAGAIN):
			continue
		case err != nil:
			return err
		case n == 0:
			return nil
		}
	}
}

// clearMap deletes every key of m. Keys are collected first, so the
// deletes never disturb the walk.
func clearMap(m *bpf.BPFMap) error {
	var keys [][]byte
	it := m.Iterator()
	for it.Next() {
		keys = append(keys, append([]byte(nil), it.Key()...))
	}
	if err := it.Err(); err != nil {
		return err
	}
	for _, k := range keys {
		if err := m.DeleteKey(unsafe.Pointer(&k[0])); err != nil && !errors.Is(err, syscall.ENOENT) {
			return err
		}
	}
	return nil
}

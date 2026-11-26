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
func (t *Tracer) SeedSockets() error {
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

// drainIter reads r until EOF. An iterator's read returns 0 at the end;
// EAGAIN never comes (the fd is blocking), and EINTR only retries.
func drainIter(r io.Reader) error {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		switch {
		case errors.Is(err, syscall.EINTR):
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

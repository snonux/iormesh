// Package bpf loads the iormesh BPF object, attaches the probe set, keeps
// the target PID map and turns the ring buffer into a channel of decoded
// internal/types records — nothing more (docs/implementation.md, M1 step
// 5). It is the tracer's cgo boundary (libbpfgo); everything downstream of
// Records is plain Go.
package bpf

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"unsafe"

	bpf "github.com/aquasecurity/libbpfgo"

	"iormesh/internal"
)

// Config is what a tracing session asks of the kernel side.
type Config struct {
	// Pids are the initial target processes (tgids). More can be added
	// with AddTarget; an empty set traces nothing (filter.c).
	Pids []uint32
	// Probes is the attach set; nil means DefaultProbes().
	Probes []Probe
	// Buffer is the capacity of the Records channel; 0 means 4096. A full
	// channel is backpressure: the ring buffer then fills and the kernel
	// counts drops (Losses.Kernel) instead of blocking the traced app.
	Buffer int
}

const (
	defaultBuffer = 4096
	// pollTimeoutMs bounds how long the poll goroutine sleeps in epoll
	// before it re-checks for Stop, i.e. how long Close can wait.
	pollTimeoutMs  = 50
	ringbufMapName = "event_map"
	targetMapName  = "target_map"
)

// Tracer is one loaded BPF object with its links and ring-buffer reader.
type Tracer struct {
	module  *bpf.Module
	links   []*bpf.BPFLink
	skipped []Probe
	rb      *bpf.RingBuffer
	records chan any
	done    chan struct{}
	decoded sync.WaitGroup
	losses  decodeLosses
	closing sync.Once
}

// Open loads the object, excludes the tracer itself, fills target_map,
// attaches cfg.Probes and starts delivering records. On any error
// everything acquired so far is released again.
func Open(cfg Config) (_ *Tracer, err error) {
	quietLibbpf()
	module, step, err := internal.LoadBPFModule()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", step, err)
	}
	t := &Tracer{module: module, done: make(chan struct{})}
	defer func() {
		if err != nil {
			err = errors.Join(err, t.Close())
		}
	}()
	if err := t.load(cfg.Pids); err != nil {
		return nil, err
	}
	probes := cfg.Probes
	if probes == nil {
		probes = DefaultProbes()
	}
	if err := t.attachAll(probes); err != nil {
		return nil, err
	}
	return t, t.startReader(cfg.Buffer)
}

// load sets the load-time globals, loads the object into the kernel and
// seeds target_map.
func (t *Tracer) load(pids []uint32) error {
	// The tracer's own I/O (ring buffer, segment writes) must never reach
	// its trace, even if a target spec ever matched it (filter.c).
	if err := t.module.InitGlobalVariable("IOR_PID_FILTER", uint32(os.Getpid())); err != nil {
		return fmt.Errorf("set IOR_PID_FILTER: %w", err)
	}
	if err := t.module.BPFLoadObject(); err != nil {
		return fmt.Errorf("load BPF object: %w", err)
	}
	for _, pid := range pids {
		if err := t.AddTarget(pid); err != nil {
			return err
		}
	}
	return nil
}

// quietLibbpf keeps libbpf's warnings (a verifier rejection among them) and
// drops its debug chatter: it logs every CO-RE relocation of ~1500
// programs.
func quietLibbpf() {
	bpf.SetLoggerCbs(bpf.Callbacks{LogFilters: []func(int, string) bool{
		func(level int, _ string) bool { return level > bpf.LibbpfWarnLevel },
	}})
}

// AddTarget starts tracing pid's syscalls.
func (t *Tracer) AddTarget(pid uint32) error {
	return t.setTarget(pid, true)
}

// RemoveTarget stops tracing pid. Records already in the ring buffer are
// still delivered.
func (t *Tracer) RemoveTarget(pid uint32) error {
	return t.setTarget(pid, false)
}

func (t *Tracer) setTarget(pid uint32, on bool) error {
	m, err := t.module.GetMap(targetMapName)
	if err != nil {
		return fmt.Errorf("get %s: %w", targetMapName, err)
	}
	key := unsafe.Pointer(&pid)
	if !on {
		if err := m.DeleteKey(key); err != nil {
			return fmt.Errorf("remove target %d: %w", pid, err)
		}
		return nil
	}
	val := uint8(1)
	if err := m.Update(key, unsafe.Pointer(&val)); err != nil {
		return fmt.Errorf("add target %d: %w", pid, err)
	}
	return nil
}

// Records delivers decoded records in ring-buffer order. Each value is a
// pointer to an internal/types record (e.g. *types.OpenEvent); the receiver
// owns it and may Recycle it. The channel is closed by Close.
func (t *Tracer) Records() <-chan any {
	return t.records
}

// Skipped lists the probes whose tracepoint this kernel lacks; they were
// left out instead of failing the session (docs/build.md).
func (t *Tracer) Skipped() []Probe {
	return append([]Probe(nil), t.skipped...)
}

// errClosed is what Losses returns after Close: the kernel counter lives in
// the module Close freed.
var errClosed = errors.New("bpf: tracer closed")

// Losses reads the cumulative loss counters. Read them before Close.
func (t *Tracer) Losses() (Losses, error) {
	select {
	case <-t.done:
		return Losses{}, errClosed
	default:
	}
	l := Losses{
		Unknown:   t.losses.unknown.Load(),
		Malformed: t.losses.malformed.Load(),
		Short:     t.losses.short.Load(),
	}
	m, err := t.module.GetMap(ringbufDropMapName)
	if err != nil {
		return l, fmt.Errorf("get %s: %w", ringbufDropMapName, err)
	}
	l.Kernel, err = readKernelDrops(m)
	return l, err
}

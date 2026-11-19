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
	// ExecWatch turns on the pattern session's exec watch (M2 step 2):
	// every exec of an untraced process is reported as a
	// *types.ExecArgvEvent and waits PENDING in target_map until the
	// caller decides with AdoptExec or IgnoreExec. Off, an untraced
	// process's exec costs nothing.
	ExecWatch bool
	// NoAggregation sends every read and write as its own enter and exit
	// records instead of folding hot loops in the kernel (rwagg.c, M2
	// step 11): a debugging switch (iormesh run -aggregate=false), which
	// gives up decision #25's loss budget on a busy loop.
	NoAggregation bool
	// NativeStacks makes every emitted syscall enter send its thread's
	// user stack id first, as a *types.StackEvent (stack.c, M2 step 9),
	// or, for a stack stack_map refused (a bucket collision), the frames
	// themselves as a *types.StackInlineEvent; StackTrace reads the stack
	// behind an id. Off, stack_map shrinks to
	// one entry and the handlers' stack walk is pruned at load.
	NativeStacks bool
	// JavaMarks makes every emitted syscall enter of a thread inside a
	// hooked JDK native send that native's mark first, as a
	// *types.JavaMarkEvent (javamark.c, M3, task td4); AttachJavaMarks
	// probes one JVM's natives. Off, java_mark_map shrinks to one entry,
	// the uprobe programs are not loaded and the handlers' lookup is
	// pruned at load.
	JavaMarks bool
	// Buffer is how many decoded records may wait for the consumer (in
	// batches of up to 256); 0 means 4096. A full channel is
	// backpressure: the ring buffer then fills and the kernel counts
	// drops (Losses.Kernel) instead of blocking the traced app.
	Buffer int
}

const (
	defaultBuffer = 4096
	// pollTimeoutMs bounds how long the reader sleeps in epoll before it
	// re-checks for Close, i.e. how long Close can wait.
	pollTimeoutMs  = 50
	ringbufMapName = "event_map"
)

// Tracer is one loaded BPF object with its links and ring-buffer reader.
type Tracer struct {
	module *bpf.Module
	links  []*bpf.BPFLink
	// progFDs are the attached programs, whose recursion misses Losses
	// counts (progmiss.go). Written while attaching, before any reader.
	progFDs []int
	skipped []Probe
	ring    *ringReader
	records chan []any
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
	if err := t.load(cfg); err != nil {
		return nil, err
	}
	probes := cfg.Probes
	if probes == nil {
		probes = DefaultProbes()
	}
	t.links, t.skipped, err = attachProbes(probes, t.attachTracepoint, (*bpf.BPFLink).Destroy)
	if err != nil {
		return nil, err
	}
	if err := t.startReader(cfg.Buffer); err != nil {
		return nil, err
	}
	return t, nil
}

// load sets the load-time globals, loads the object into the kernel and
// seeds target_map.
func (t *Tracer) load(cfg Config) error {
	// The tracer's own I/O (ring buffer, segment writes) must never reach
	// its trace, even if a target spec ever matched it (filter.c).
	if err := t.module.InitGlobalVariable("IOR_PID_FILTER", uint32(os.Getpid())); err != nil {
		return fmt.Errorf("set IOR_PID_FILTER: %w", err)
	}
	watch := uint32(0)
	if cfg.ExecWatch {
		watch = 1
	}
	if err := t.module.InitGlobalVariable("IORMESH_EXEC_WATCH", watch); err != nil {
		return fmt.Errorf("set IORMESH_EXEC_WATCH: %w", err)
	}
	aggregate := uint32(1)
	if cfg.NoAggregation {
		aggregate = 0
	}
	if err := t.module.InitGlobalVariable("IORMESH_RW_AGGREGATE", aggregate); err != nil {
		return fmt.Errorf("set IORMESH_RW_AGGREGATE: %w", err)
	}
	if err := t.setNativeStacks(cfg.NativeStacks); err != nil {
		return err
	}
	if err := t.setJavaMarks(cfg.JavaMarks); err != nil {
		return err
	}
	if err := t.module.BPFLoadObject(); err != nil {
		return fmt.Errorf("load BPF object: %w", err)
	}
	for _, pid := range cfg.Pids {
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

// Records delivers decoded records in ring-buffer order, in batches of
// those that arrived together (never empty). Each value is a pointer to
// an internal/types record (e.g. *types.OpenEvent); the receiver owns
// batch and records and may Recycle them (types.Recycle). The channel is
// closed by Close.
func (t *Tracer) Records() <-chan []any {
	return t.records
}

// Skipped lists the probes whose tracepoint this kernel lacks; they were
// left out instead of failing the session (docs/build.md).
func (t *Tracer) Skipped() []Probe {
	return append([]Probe(nil), t.skipped...)
}

// errClosed is what the map-touching methods return after Close: the maps
// live in the module Close freed.
var errClosed = errors.New("bpf: tracer closed")

// closed reports whether Close has started. AddTarget, RemoveTarget and
// Losses check it, but must still not race Close itself: a call that passed
// the check while Close frees the module would touch freed memory.
func (t *Tracer) closed() bool {
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}

// Losses reads the cumulative loss counters. Read them before Close: the
// records Close drops in flight are counted nowhere, by design, since the
// counters die with the module.
func (t *Tracer) Losses() (Losses, error) {
	if t.closed() {
		return Losses{}, errClosed
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
	if l.Kernel, err = readKernelDrops(m); err != nil {
		return l, err
	}
	l.Missed, err = readProgMisses(t.progFDs, progMisses)
	return l, err
}

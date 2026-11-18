// Package correlate turns the raw BPF records of internal/bpf into schema
// events and the per-host graph (docs/implementation.md, M1 step 6). It
// keeps pid→fd→path and pid→fd→socket state, pairs syscall enters with
// their exits, turns marker opens into marker events and fuses the two
// ends of a loopback connection into one conn id.
//
// One goroutine owns a Correlator: Handle, Close and Graph must not be
// called concurrently. The package is cgo-free; it only sees decoded
// internal/types values.
package correlate

import (
	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// Host identifies where and for which project the trace runs.
type Host struct {
	ID      string // /etc/machine-id
	Project string
}

// ProcInfo returns a process's comm and its start time on the boot clock
// (the pid incarnation). It is a parameter so tests need no /proc.
type ProcInfo func(pid uint32) (comm string, startBootNS uint64)

// Config configures a Correlator.
type Config struct {
	Host Host
	Sink schema.Sink
	// ProcInfo defaults to ReadProcInfo.
	ProcInfo ProcInfo
	// WallOffsetNS converts boot-clock to wall-clock time: wall = boot +
	// offset. The caller measures it once at start (BootToWallOffset).
	WallOffsetNS int64
	// FuseWindowNS is how long a socket event waits for its connection's
	// other end before it is emitted as unresolved; 0 means 5s, the
	// window the plan gives the loopback join.
	FuseWindowNS uint64
}

const defaultFuseWindowNS = 5_000_000_000

// Correlator holds the per-host state between records.
type Correlator struct {
	cfg      Config
	seq      uint64
	now      uint64 // boot ns of the newest record seen
	procs    map[uint32]*procState
	socks    *sockTable
	enters   map[uint32]enter // pending syscall enters by tid
	queue    []queued         // emitted in seq order, held for conn ids
	graph    *graphBuilder
	unpaired uint64
}

// New returns a Correlator writing to cfg.Sink.
func New(cfg Config) *Correlator {
	if cfg.ProcInfo == nil {
		cfg.ProcInfo = ReadProcInfo
	}
	if cfg.FuseWindowNS == 0 {
		cfg.FuseWindowNS = defaultFuseWindowNS
	}
	return &Correlator{
		cfg:    cfg,
		procs:  map[uint32]*procState{},
		socks:  newSockTable(cfg.FuseWindowNS),
		enters: map[uint32]enter{},
		graph:  newGraphBuilder(cfg.Host.ID),
	}
}

// Handle consumes one record from bpf.Tracer.Records. Record types the M1
// probe set does not produce are ignored. The error is the sink's.
func (c *Correlator) Handle(rec any) error {
	switch r := rec.(type) {
	case *types.OpenEvent:
		c.enterOpen(r)
	case *types.FdEvent:
		c.enterFd(r)
	case *types.AcceptEvent:
		c.accept(r)
	case *types.RetEvent:
		c.exitRet(r)
	case *types.TcpStateEvent:
		c.tcpState(r)
	default:
		return nil
	}
	return c.release(false)
}

// Unpaired counts exits that arrived without their enter (or with a
// different syscall's enter pending), e.g. after a ring-buffer drop. Such
// exits produce no event: without the enter, fd and path are unknown.
func (c *Correlator) Unpaired() uint64 {
	return c.unpaired
}

// Close emits every held event (connections still unfused become
// unresolved) and closes the sink.
func (c *Correlator) Close() error {
	if err := c.release(true); err != nil {
		_ = c.cfg.Sink.Close()
		return err
	}
	return c.cfg.Sink.Close()
}

// Graph returns the graph of everything emitted so far, sorted.
func (c *Correlator) Graph() schema.Graph {
	return c.graph.build()
}

// advance moves the correlator's clock; records are not strictly ordered
// across CPUs, so the clock only ever moves forward.
func (c *Correlator) advance(bootNS uint64) {
	if bootNS > c.now {
		c.now = bootNS
	}
}

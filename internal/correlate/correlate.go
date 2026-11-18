// Package correlate turns the raw BPF records of internal/bpf into schema
// events and the per-host graph (docs/implementation.md, M1 step 6). It
// keeps pid→fd→path state that follows renames (M2 step 5, paths.go) and
// pid→fd→socket state through dup, fork and exec (M2 step 6, fdtable.go),
// pairs syscall enters with their exits, turns
// marker opens into marker events and fuses the two ends of a loopback
// connection into one conn id. Around the syscall
// events it numbers the host-level ones the caller reports: the session
// header (Session), ring-buffer loss (RecordLosses) and the end (End).
//
// One goroutine owns a Correlator: Handle, Close and Graph must not be
// called concurrently. The package is cgo-free; it only sees decoded
// internal/types values.
package correlate

import (
	"net/netip"

	"iormesh/internal/redact"
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
	// Clock stamps session, drop and end events; it defaults to BootNow.
	Clock Clock
	// Redaction masks secrets in exec argv (decision #9). nil means
	// redact.Default(): a pointer, so a zero Config can never mean "no
	// redaction".
	Redaction *redact.Policy
}

const defaultFuseWindowNS = 5_000_000_000

// Correlator holds the per-host state between records.
type Correlator struct {
	cfg      Config
	redact   redact.Policy
	seq      uint64
	now      uint64 // boot ns of the newest record seen
	procs    map[uint32]*procState
	socks    *sockTable
	datagram *sockTable                       // UDP flows, apart from TCP: one tuple may be both
	unix     *unixTable                       // AF_UNIX stream connections by their socket pair
	flows    map[[2]netip.AddrPort]*sockState // UDP flows by (local, remote)
	enters   map[uint32]enter                 // pending syscall enters by tid
	queue    []queued                         // emitted in seq order, held for conn ids
	graph    *graphBuilder
	unpaired uint64
	noPath   uint64
	lost     uint64 // cumulative losses already reported by drop events
	ended    bool   // End was called: nothing may follow the end event
}

// New returns a Correlator writing to cfg.Sink.
func New(cfg Config) *Correlator {
	if cfg.ProcInfo == nil {
		cfg.ProcInfo = ReadProcInfo
	}
	if cfg.FuseWindowNS == 0 {
		cfg.FuseWindowNS = defaultFuseWindowNS
	}
	if cfg.Clock == nil {
		cfg.Clock = BootNow
	}
	policy := redact.Default()
	if cfg.Redaction != nil {
		policy = *cfg.Redaction
	}
	return &Correlator{
		cfg:      cfg,
		redact:   policy,
		procs:    map[uint32]*procState{},
		socks:    newSockTable(cfg.FuseWindowNS),
		datagram: newSockTable(cfg.FuseWindowNS),
		unix:     newUnixTable(),
		flows:    map[[2]netip.AddrPort]*sockState{},
		enters:   map[uint32]enter{},
		graph:    newGraphBuilder(cfg.Host.ID),
	}
}

// Handle consumes one record from bpf.Tracer.Records: syscall records
// (M1's, and the path, name, dup3 and fcntl enters of M2's file surface),
// TCP state records, the process lifecycle records of M2 steps 1 and 6
// (new processes, execs, exits) and M2 step 7's records (handleIPC:
// pipes, socketpairs, kill, epoll and poll, socket addresses, signals).
// Other record types are ignored; the exec_argv records of a pattern session are the caller's to judge
// (Incarnation), and an exec's argv reaches the trace through Exec, once
// the caller has completed a truncated one. The error is the sink's, or
// ErrEnded after End.
func (c *Correlator) Handle(rec any) error {
	if c.ended {
		return ErrEnded
	}
	switch r := rec.(type) {
	case *types.OpenEvent:
		c.enterOpen(r)
	case *types.OpenNameFixupEvent:
		c.fixupName(r)
	case *types.FdEvent:
		c.enterFd(r)
	case *types.PathEvent:
		c.enterPath(r)
	case *types.NameEvent:
		c.enterName(r)
	case *types.Dup3Event:
		c.enterDup3(r)
	case *types.FcntlEvent:
		c.enterFcntl(r)
	case *types.AcceptEvent:
		c.accept(r)
	case *types.RetEvent:
		c.exitRet(r)
	case *types.TcpStateEvent:
		c.tcpState(r)
	case *types.TaskNewtaskEvent:
		c.newTask(r)
	case *types.ProcessExitEvent:
		c.exitTask(r)
	case *types.ProcessExecEvent:
		c.execTask(r)
	default:
		if !c.handleIPC(rec) {
			return nil
		}
	}
	return c.release(false)
}

// Unpaired counts exits that arrived without their enter (or with a
// different syscall's enter pending), e.g. after a ring-buffer drop. Such
// exits produce no event: without the enter, fd and path are unknown.
func (c *Correlator) Unpaired() uint64 {
	return c.unpaired
}

// NoPath counts opens whose path could not be read (a NULL or faulting
// pointer the exit-time re-read could not recover either). They produce
// no event — an open without its path says nothing — but their fd is
// still tracked.
func (c *Correlator) NoPath() uint64 {
	return c.noPath
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

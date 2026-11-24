// Package correlate turns the raw BPF records of internal/bpf into schema
// events and the per-host graph (docs/implementation.md, M1 step 6). It
// keeps pid→fd→path state that follows renames (M2 step 5, paths.go) and
// pid→fd→socket state through dup, fork and exec (M2 step 6, fdtable.go),
// pairs syscall enters with their exits, expands the kernel's folded
// read/write records (M2 step 11, aggregate.go), turns
// marker opens into marker events and fuses the two ends of a loopback
// connection into one conn id. Around the syscall
// events it numbers the host-level ones the caller reports: the session
// header (Session), ring-buffer and decoder loss (RecordLosses), the
// program runs the kernel skipped (RecordSkips) and the end (End).
//
// With Config.Stacks it also names each syscall event's native stack
// (M2 step 9, stacks.go).
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
	ID      string // host_id: /etc/machine-id, or "pod-<uid>" in a pod
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
	// window the plan gives the loopback join. The Java join waits for
	// it before it releases an http record by the clock
	// (JavaJoinConfig.UpstreamHold, which must equal it): change both.
	FuseWindowNS uint64
	// Clock stamps session, drop and end events; it defaults to BootNow.
	Clock Clock
	// Redaction masks secrets in exec argv (decision #9). nil means
	// redact.Default(): a pointer, so a zero Config can never mean "no
	// redaction".
	Redaction *redact.Policy
	// Stacks, when set, turns the kernel's stack records into stack_id
	// fields (M2 step 9, -native-stacks; stacks.go). nil: stack records
	// are ignored, as they never come without the switch anyway.
	Stacks StackIDs
	// NoGraph skips the per-host graph: Graph then returns an empty one.
	// A run without -out writes no graph.json (on a server the
	// controller rebuilds the graph from the segments, decision #3), and
	// a graph kept anyway grows by a node and an edge per distinct file
	// path, connection and process for the whole session (task 4j4).
	// The events do not depend on it. false keeps the graph, so a zero
	// Config builds one.
	NoGraph bool
	// Pod, set for an in-pod tracer, stamps every event with the pod
	// object (PLAN.md M7 "Pod fields on events", task sh4; pod.go): the
	// pod's namespace and name, and on a process's events its container
	// (ContainerOf). nil outside a pod: no event has a pod object and no
	// cgroup is read.
	Pod *PodConfig
	// ContainerOf returns the container id of pid's process, "" for
	// none (k8s.ContainerOf over /proc/<pid>/cgroup in a run). It is
	// asked once per process incarnation, when the correlator first
	// stamps one of its events; a forked child inherits its parent's
	// answer. nil: no event names a container. Only used with Pod.
	ContainerOf func(pid uint32) string
}

const defaultFuseWindowNS = 5_000_000_000

// Correlator holds the per-host state between records.
type Correlator struct {
	cfg     Config
	redact  redact.Policy
	seq     uint64
	now     uint64 // boot ns of the newest record seen
	recTime uint64 // boot ns of the record being handled (procState.clock)
	procs   map[uint32]*procState
	// lastPID and lastProc cache proc's last answer.
	lastPID  uint32
	lastProc *procState
	// dying lists dead processes waiting out their fuse window (reap.go).
	dying    []dying
	socks    *sockTable
	datagram *sockTable                       // UDP flows, apart from TCP: one tuple may be both
	unix     *unixTable                       // AF_UNIX stream connections by their socket pair
	flows    map[[2]netip.AddrPort]*sockState // UDP flows by (local, remote)
	enters   map[uint32]enter                 // pending syscall enters by tid
	stacks   map[uint32]stackRef              // stacks awaiting their enter, by tid
	queue    []queued                         // emitted in seq order, held for conn ids
	graph    *graphBuilder
	unpaired uint64
	noPath   uint64
	// unattributed counts folded read/write records no fd instance
	// explains (aggregate.go).
	unattributed uint64
	lost         uint64 // cumulative losses already reported by drop events
	skipped      uint64 // cumulative skipped program runs already reported
	ended        bool   // End was called: nothing may follow the end event
	// pods are the pod objects events share (pod.go): hostPod, the pod
	// alone, for host-level events and processes in no container, and
	// one per container id seen. nil hostPod: not in a pod.
	hostPod *schema.Pod
	pods    map[string]*schema.Pod
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
	var graph *graphBuilder // nil: the graph is off (graphBuilder)
	if !cfg.NoGraph {
		graph = newGraphBuilder(cfg.Host.ID)
	}
	c := &Correlator{
		cfg:      cfg,
		redact:   policy,
		procs:    map[uint32]*procState{},
		socks:    newSockTable(cfg.FuseWindowNS),
		datagram: newSockTable(cfg.FuseWindowNS),
		unix:     newUnixTable(),
		flows:    map[[2]netip.AddrPort]*sockState{},
		enters:   map[uint32]enter{},
		stacks:   map[uint32]stackRef{},
		graph:    graph,
	}
	if cfg.Pod != nil {
		c.hostPod = &schema.Pod{Namespace: cfg.Pod.Namespace, Name: cfg.Pod.Name}
		c.pods = map[string]*schema.Pod{}
	}
	return c
}

// Handle consumes one record from bpf.Tracer.Records: syscall records
// (M1's, and the path, name, dup3 and fcntl enters of M2's file surface),
// TCP state records, the process lifecycle records of M2 steps 1 and 6
// (new processes, execs, exits) and comm renames (task 0g4), M2 step 7's
// records (handleIPC: pipes, socketpairs, kill, epoll and poll, socket
// addresses, signals),
// M2 step 9's native stack records (stacks.go) and M4's TCP sequence
// records (streamseq.go).
// Other record types are ignored; the exec_argv records of a pattern
// session are the caller's to judge (Incarnation), and an exec's argv
// reaches the trace through Exec, once the caller has completed a
// truncated one. The error is the sink's, or
// ErrEnded after End. Handle keeps no reference to rec (it copies what it
// needs), so the caller may recycle it once Handle returns
// (types.Recycle).
//
// The dispatch is split by record family (handleFile, handleNet,
// handleTask, handleStack, handleIPC), each reporting whether rec was one
// of its types; the families' types are disjoint, so their order only
// matters in that a stack record's error returns before reap and release.
func (c *Correlator) Handle(rec any) error {
	if c.ended {
		return ErrEnded
	}
	handled, err := c.dispatch(rec)
	if err != nil || !handled {
		return err
	}
	c.reap()
	return c.release(false)
}

// dispatch hands rec to its family's handler and reports whether one
// took it; an unknown record is ignored (false, nil).
func (c *Correlator) dispatch(rec any) (bool, error) {
	if c.handleFile(rec) || c.handleNet(rec) || c.handleTask(rec) {
		return true, nil
	}
	if handled, err := c.handleStack(rec); handled || err != nil {
		return handled, err
	}
	return c.handleIPC(rec), nil
}

// handleFile dispatches the syscall enter and exit records of the file
// surface (M1, M2) and reports whether rec was one of them.
func (c *Correlator) handleFile(rec any) bool {
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
	case *types.RetEvent:
		c.exitRet(r)
	case *types.RwAggregateEvent:
		c.aggregate(r)
	default:
		return false
	}
	return true
}

// handleNet dispatches the socket records: accepts, TCP state and
// sequence records (streamseq.go) and the mmsg batches, and reports
// whether rec was one of them.
func (c *Correlator) handleNet(rec any) bool {
	switch r := rec.(type) {
	case *types.AcceptEvent:
		c.accept(r)
	case *types.TcpStateEvent:
		c.tcpState(r)
	case *types.TcpSeqEvent:
		c.tcpSeqRecord(r)
	case *types.MmsgEvent:
		c.mmsgRecord(r)
	default:
		return false
	}
	return true
}

// handleTask dispatches the process lifecycle records (new tasks, exits,
// execs) and comm renames, and reports whether rec was one of them.
func (c *Correlator) handleTask(rec any) bool {
	switch r := rec.(type) {
	case *types.TaskNewtaskEvent:
		c.newTask(r)
	case *types.ProcessExitEvent:
		c.exitTask(r)
	case *types.ProcessExecEvent:
		c.execTask(r)
	case *types.TaskRenameEvent:
		c.renameTask(r)
	default:
		return false
	}
	return true
}

// handleStack dispatches the native stack records (stacks.go): handled
// reports whether rec was one, err is the stack handler's.
func (c *Correlator) handleStack(rec any) (handled bool, err error) {
	switch r := rec.(type) {
	case *types.StackEvent:
		return true, c.stackRecord(r)
	case *types.StackInlineEvent:
		return true, c.stackInlineRecord(r)
	}
	return false, nil
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

// Graph returns the graph of everything emitted so far, sorted; an empty
// graph with Config.NoGraph.
func (c *Correlator) Graph() schema.Graph {
	return c.graph.build()
}

// Tick advances the correlator's clock to Config.Clock and releases
// what that decides: a socket event whose fuse window passed leaves as
// unresolved, with every event held behind it, and a dead process whose
// window passed is forgotten (reap). The clock otherwise only
// moves with records, so without Tick a connection whose other end is on
// another host would hold the whole stream until the next record — 30 s
// on a server called every 30 s, not the 5 s window (task 1f4 review).
// The run loop calls it on its ticker, and only while no record batch is
// waiting: a backlog is the stream behind the clock, and the window must
// not run out on records not yet read.
func (c *Correlator) Tick() error {
	if c.ended {
		return nil
	}
	if now := c.cfg.Clock(); now > c.now {
		c.now = now
	}
	c.reap()
	return c.release(false)
}

// advance moves the correlator's clock; records are not strictly ordered
// across CPUs, so the clock only ever moves forward. recTime is the
// record's own time, which dates the fd instances it creates or ends.
func (c *Correlator) advance(bootNS uint64) {
	c.recTime = bootNS
	if bootNS > c.now {
		c.now = bootNS
	}
}

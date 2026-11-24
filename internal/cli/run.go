package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/pprof"
	"syscall"
	"time"

	"iormesh/internal/bpf"
	"iormesh/internal/correlate"
	"iormesh/internal/k8s"
	"iormesh/internal/schema"
	"iormesh/internal/target"
	"iormesh/internal/types"
)

// liveCheckEvery is how often run checks whether its targets still exist;
// quietFor is how long it keeps draining once they are all gone, so the
// records of their last syscalls are not lost.
// stopDrainMax bounds the drain after a signal or the duration ended the
// run: the records already in flight are handled (their targets' last
// syscalls, say), but a busy target cannot hold a stop up.
const (
	liveCheckEvery = 200 * time.Millisecond
	quietFor       = 300 * time.Millisecond
	stopDrainMax   = 2 * time.Second
)

func runCommand(args []string, stderr io.Writer) error {
	cfg, err := parseRunFlags(args, stderr)
	if err != nil {
		return err
	}
	// The installed binary run through sudo is confined (confine.go): the
	// sudoers rule must not be a way to write files as root.
	if err := confineRun(cfg); err != nil {
		return err
	}
	if cfg.clockSkew, err = runClockSkew(); err != nil {
		return err
	}
	if cfg.clockSkew != 0 {
		// Loud on purpose: a skewed trace must never pass for a real one.
		_, _ = fmt.Fprintf(stderr, "iormesh: TEST ONLY: wall clock skewed by %v (%s)\n",
			cfg.clockSkew, testClockSkewEnv)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return runSession(ctx, cfg, stderr)
}

// startCPUProfile starts a pprof CPU profile into path ("" profiles
// nothing) and returns the function that stops and writes it. It covers
// the whole run, BPF load included; the per-event share is what the
// correlator's and sink's frames add up to.
func startCPUProfile(path string) (stop func() error, err error) {
	if path == "" {
		return func() error { return nil }, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("cpuprofile: %w", err)
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		return nil, errors.Join(fmt.Errorf("cpuprofile: %w", err), f.Close())
	}
	return func() error {
		pprof.StopCPUProfile()
		return f.Close()
	}, nil
}

// tracer is what run needs of bpf.Tracer; tests substitute a fake to
// drive the loop without a kernel.
type tracer interface {
	Records() <-chan []any
	Release(batch []any)
	Losses() (bpf.Losses, error)
	AddTarget(pid uint32) error
	RemoveTarget(pid uint32) error
	AdoptExec(pid uint32) (gap uint64, adopted bool, err error)
	IgnoreExec(pid uint32, since uint64, alive func() bool) error
	StopAggregation() error
	PendingAggregates() ([]*types.RwAggregateEvent, error)
	FinalSockets() ([]*types.TcpStateEvent, error)
	StackTrace(id uint32) ([]uint64, error)
	AttachJavaMarks(pid uint32, natives []bpf.JavaNative) ([]string, error)
	DetachJavaMarks(pid uint32) error
	Skipped() []bpf.Probe
	PidNS() uint32
	Close() error
}

var _ tracer = (*bpf.Tracer)(nil)

// runTrace traces cfg.spec's targets until the duration ends, a signal
// arrives or (for PIDs and a port) every target has exited — a port
// session only once no restarted owner appeared within -listen-grace —
// then writes the event stream and the graph into cfg.out. The stream opens with a
// session header, records loss as drop events while it runs and closes
// with an end event naming why it stopped (PLAN.md, "Trace output
// contract").
//
// The tracer is opened before the output files are created, so a run that
// cannot attach (no root, no BTF) leaves an existing trace directory
// untouched instead of truncating its events next to an old graph.
func runTrace(ctx context.Context, cfg runConfig, stderr io.Writer) error {
	if err := preflight(cfg); err != nil {
		return err
	}
	pids, act, err := initialTargets(cfg)
	if err != nil {
		return err
	}
	tr, err := openTracer(bpf.Config{Pids: pids, ExecWatch: cfg.spec.Mode() == target.ModePattern,
		Probes: bpf.ProbesFor(cfg.families), NoAggregation: cfg.noAggregation,
		NativeStacks: cfg.nativeStacks, JavaMarks: cfg.spool != ""})
	if err != nil {
		return err
	}
	out, err := openOutputs(ctx, cfg, tr, stderr)
	if err != nil {
		return errors.Join(err, tr.Close())
	}
	c, err := newCorrelator(cfg, out)
	if err != nil {
		return errors.Join(err, out.close(), tr.Close())
	}
	s := newSession(tr, c, cfg)
	s.java, s.seg, s.log = out.java, out.seg, stderr
	s.awaitService(act)
	// end stops the late attaches (task lh4) before finish closes the
	// Java join, so their stages are removed with the session's.
	end := func(reason string, err error) error {
		s.stopLate()
		return errors.Join(err, finish(cfg, tr, c, out, reason, stderr))
	}
	if err := s.start(pids); err != nil {
		return end(schema.EndError, err)
	}
	_, _ = fmt.Fprintf(stderr, "iormesh: attached, tracing %s (%d tracepoints missing on this kernel)\n",
		describe(cfg.spec, pids), len(tr.Skipped()))
	announcePidNS(tr.PidNS(), stderr)
	if err := s.startJava(ctx, cfg, stderr); err != nil {
		return end(schema.EndError, err)
	}
	// state.json says running before the ready file appears, so a harness
	// released by the one finds the other.
	if err := out.seg.markRunning(cfg, describe(cfg.spec, pids), time.Now()); err != nil {
		return end(schema.EndError, err)
	}
	if err := announceReady(cfg); err != nil {
		return end(schema.EndError, err)
	}
	return end(traceLoop(ctx, s, cfg))
}

// preflight runs before anything is loaded: a stale ready file is
// removed, so a harness reusing the path never mistakes a previous run's
// for this one's, and a trace disk already over its limit fails the run
// at once (checkTraceDisk) — before any BPF load. In a pod the process
// then still waits for collect to drain the dir (runSession,
// awaitDrain) rather than exit into a kubelet restart loop.
func preflight(cfg runConfig) error {
	if cfg.ready != "" {
		if err := os.Remove(cfg.ready); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("ready file: %w", err)
		}
	}
	return checkTraceDisk(cfg)
}

// announcePidNS says once that a trace's pids are a PID namespace's (ns, its
// inode; M7, task 4f4): in a pod every pid is the pod's own number, not the
// node's. A host run (ns 0) prints nothing more than before.
func announcePidNS(ns uint32, stderr io.Writer) {
	if ns != 0 {
		_, _ = fmt.Fprintf(stderr, "iormesh: pids are numbered in PID namespace pid:[%d], not the host's\n", ns)
	}
}

// outputs are where a run writes besides -out's events file: the parquet
// segments (-trace-dir), the native stack table (-native-stacks) and the
// Java join's tables (-spool), each nil when its switch is off; and the
// host the events are stamped with.
type outputs struct {
	seg     *segOutput
	stacks  *stackOutput
	java    *javaOutput
	hostID  string
	session schema.Session
	// pod is the pod the run is in (podOf), nil outside one: the pod
	// fields of its events (task sh4).
	pod *k8s.PodRef
}

// openOutputs describes the host — before any trace file is touched, so
// a host it cannot describe leaves an old trace intact — then opens the
// segment writer (named by the host's FQDN) and the tables, which write
// into it too.
func openOutputs(ctx context.Context, cfg runConfig, tr tracer, stderr io.Writer) (outputs, error) {
	var o outputs
	var err error
	if o.hostID, o.session, err = describeHost(ctx, podEnv); err != nil {
		return o, err
	}
	o.pod = podOf(podEnv)
	if o.seg, err = openSegments(cfg, o.session.FQDN, stderr); err != nil {
		return o, err
	}
	if o.stacks, err = openStacks(cfg, tr, o.seg); err != nil {
		return o, errors.Join(err, o.close())
	}
	if o.java, err = openJava(cfg, tr, o.seg, stderr); err != nil {
		return o, errors.Join(err, o.close())
	}
	return o, nil
}

// close closes the tables, then the segment writer: its last segment
// must take the stack table's last rows, which its worker writes until
// the table closes.
func (o outputs) close() error {
	return errors.Join(o.stacks.close(), o.java.close(), o.seg.close())
}

func (o outputs) summary() string {
	return o.stacks.summary() + o.java.summary()
}

// initialTargets resolves the processes target_map starts with: the
// PIDs, or the owners of the listening port (never a service manager;
// once the last of them is gone the session re-resolves the port for a
// restarted owner, portfollow.go, decision #23). A pattern starts empty;
// its matches are added after the probes attached (session.start), so
// none can exec unseen in between. A port only the service manager holds
// (an idle socket-activated service) starts with no target and its
// listener pinned, unless -listen-grace is 0 (portActivation, task lk4).
func initialTargets(cfg runConfig) ([]uint32, *target.Activation, error) {
	spec := cfg.spec
	if spec.Mode() == target.ModePattern {
		return nil, nil, nil
	}
	pids, err := spec.Resolve(target.HostProcFS(), uint32(os.Getpid()))
	if err == nil || spec.Mode() != target.ModeListenPort {
		return pids, nil, err
	}
	act, err := portActivation(cfg, err)
	return nil, act, err
}

// describe names the targets for the attach line.
func describe(spec target.Spec, pids []uint32) string {
	switch spec.Mode() {
	case target.ModePattern:
		return fmt.Sprintf("pattern %q", spec.Pattern)
	case target.ModeListenPort:
		if len(pids) == 0 { // only the service manager holds it (task lk4)
			return fmt.Sprintf("port %d, waiting for its socket-activated service", spec.ListenPort)
		}
		return fmt.Sprintf("port %d owners, pid %v", spec.ListenPort, pids)
	}
	return fmt.Sprintf("pid %v", pids)
}

// finish ends a trace: the folded calls the kernel still holds, the
// final snapshot of every open socket (finalSockets), a last loss poll
// (the counters die with the tracer), the tracer's close, the end event, the sink's close (the Java
// join's last ring read with it), the graph file, state.json's end and
// the optional tables' close, then the one-line summary. A loss count it
// cannot read turns the end reason into error: the trace might hide a
// gap.
func finish(cfg runConfig, tr tracer, c *correlate.Correlator, out outputs, reason string,
	stderr io.Writer) error {
	aggErr := drainAggregates(tr, c)
	if aggErr == nil {
		aggErr = finalSockets(tr, c, stderr)
	}
	losses, lossErr := pollLosses(tr, c)
	if lossErr != nil || aggErr != nil {
		reason = schema.EndError
	}
	closeErr := tr.Close()
	endErr := c.End(reason)
	sinkErr := c.Close()
	// The graph is -out's: on a server the controller rebuilds it from the
	// segments (decision #3), so -trace-dir alone writes none and its
	// correlator never builds one (correlatorConfig).
	var graphErr error
	if cfg.out != "" {
		graphErr = schema.WriteGraphFile(filepath.Join(cfg.out, GraphFile), c.Graph())
	}
	// state.json's end goes first: once the writer has closed, its lock
	// is free and the project's next run may own the state.
	stateErr := out.seg.markEnded(reason, time.Now())
	tablesErr := out.close()
	// Skipped runs are host-wide possible loss, reported beside the lost
	// records and never in them (bpf.Losses.Missed, task vd4).
	_, _ = fmt.Fprintf(stderr, "iormesh: stopped (%s); lost records %d (kernel %d), skipped runs %d, "+
		"unpaired exits %d, unreadable paths %d, unattributed aggregates %d%s\n",
		reason, losses.Total(), losses.Kernel, losses.Missed, c.Unpaired(), c.NoPath(), c.Unattributed(),
		out.summary())
	return errors.Join(aggErr, lossErr, closeErr, endErr, sinkErr, graphErr, stateErr, tablesErr)
}

// drainAggregates hands the correlator the folded read/write calls the
// kernel still holds (bpf.Tracer.PendingAggregates): those of threads that
// reached no flush point before the trace ended. Without it their calls
// would be missing from the trace, with no drop to say so. An unreadable
// map ends the trace as error for the same reason.
func drainAggregates(tr tracer, c *correlate.Correlator) error {
	// Normally stopped by traceLoop before its drain already; a trace that
	// ends early (an error) stops here, and a second stop is harmless.
	if err := tr.StopAggregation(); err != nil {
		return fmt.Errorf("stop aggregation: %w", err)
	}
	recs, err := tr.PendingAggregates()
	for _, rec := range recs {
		if herr := c.Handle(rec); herr != nil {
			return herr
		}
	}
	if err != nil {
		return fmt.Errorf("pending aggregates: %w", err)
	}
	return nil
}

// finalSockets hands the correlator the tracer's final snapshot of every
// TCP socket still open (bpf.Tracer.FinalSockets, task sf4): the byte
// counters after the session's last recorded call, which the merge's
// counter proof needs for a connection no CLOSE record ended
// (internal/federate/clockproof.go). Taken after the last records were
// handled and the folded calls drained, so each snapshot follows every
// call of its socket the trace holds. A snapshot that cannot be taken is
// said on stderr and does not fail the trace: the app's records are all
// there, and the merge only proves fewer messages without it — the
// stream check still covers them where it holds. A correlator error is
// the trace's, as for any record.
func finalSockets(tr tracer, c *correlate.Correlator, stderr io.Writer) error {
	recs, err := tr.FinalSockets()
	for _, rec := range recs {
		if herr := c.Handle(rec); herr != nil {
			return herr
		}
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "iormesh: warning: final socket snapshot: %v\n", err)
	}
	return nil
}

// newCorrelator creates the correlator writing the events — to cfg.out's
// events file and/or the segments, through the Java join with -spool —
// and queues the session header. out.stacks is the native stack table,
// nil without -native-stacks.
func newCorrelator(cfg runConfig, out outputs) (*correlate.Correlator, error) {
	offset, err := correlate.BootToWallOffset()
	if err != nil {
		return nil, fmt.Errorf("boot clock: %w", err)
	}
	// Every wall_ns derives from this one offset (Java's too, from the
	// events), so the test-only skew moves the whole host's wall clock
	// and nothing else.
	offset += int64(cfg.clockSkew)
	events, err := openEvents(cfg)
	if err != nil {
		return nil, err
	}
	c := correlate.New(correlatorConfig(cfg, out, out.java.sink(out.seg.sink(events)), offset))
	if err := c.Session(out.session); err != nil {
		return nil, errors.Join(err, c.Close())
	}
	return c, nil
}

// correlatorConfig is the correlator's configuration for a run writing to
// sink. Without -out it builds no graph: finish writes graph.json only
// into cfg.out, and on a server the controller rebuilds the graph from
// the segments (decision #3), so a graph kept by a -trace-dir run would
// only grow with every distinct path, connection and process (task 4j4).
// In a pod it also carries the pod and the container reader (podConfig).
func correlatorConfig(cfg runConfig, out outputs, sink schema.Sink, offset int64) correlate.Config {
	c := correlate.Config{Host: correlate.Host{ID: out.hostID, Project: cfg.project},
		Sink: sink, WallOffsetNS: offset, Stacks: out.stacks.ids(), NoGraph: cfg.out == ""}
	podConfig(&c, out.pod) // in a pod: the pod fields of every event (task sh4)
	return c
}

// openEvents creates cfg.out and its NDJSON events sink; nil without
// -out.
func openEvents(cfg runConfig) (schema.Sink, error) {
	if cfg.out == "" {
		return nil, nil
	}
	if err := os.MkdirAll(cfg.out, 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(filepath.Join(cfg.out, EventsFile))
	if err != nil {
		return nil, err
	}
	return schema.NewNDJSONSink(f), nil
}

// announceReady creates cfg.ready once every probe is attached.
func announceReady(cfg runConfig) error {
	if cfg.ready == "" {
		return nil
	}
	f, err := os.Create(cfg.ready)
	if err != nil {
		return fmt.Errorf("ready file: %w", err)
	}
	return f.Close()
}

// pollLosses turns growth of the tracer's loss counters into drop
// events, so a gap is visible in the stream close to where it happened:
// lost records as plain drops, skipped program runs as skipped_runs drops
// of their own, since those are host-wide and only bound the trace's loss
// (task vd4).
func pollLosses(tr tracer, c *correlate.Correlator) (bpf.Losses, error) {
	l, err := tr.Losses()
	if err != nil {
		return l, fmt.Errorf("loss counters: %w", err)
	}
	if err := c.RecordLosses(l.Total()); err != nil {
		return l, err
	}
	return l, c.RecordSkips(l.Missed)
}

// traceLoop feeds records to the session until a stop condition and
// returns why it stopped, as a schema end reason. Before the final drain it
// freezes the kernel's read/write aggregation (bpf.Tracer.StopAggregation),
// so finish reads sums that can no longer also reach the ring. A sink error stops the
// trace at once: tracing on into a broken file would only hide the loss.
// So does an unreadable loss counter, since drops must never be silent.
func traceLoop(ctx context.Context, s *session, cfg runConfig) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.duration)
	defer cancel()
	tick := time.NewTicker(liveCheckEvery)
	defer tick.Stop()
	for {
		select {
		case batch, ok := <-s.tr.Records():
			if !ok {
				// Only Close ends the stream, and run has not closed it.
				return schema.EndError, errors.New("record stream closed unexpectedly")
			}
			if err := s.handleAll(batch); err != nil {
				return schema.EndError, err
			}
		case <-ctx.Done():
			return ctxReason(ctx), errors.Join(s.tr.StopAggregation(), drainStopped(s))
		case <-tick.C:
			if err := s.poll(); err != nil {
				return endReason(err)
			}
			done, err := s.ended()
			if err != nil {
				return schema.EndError, err
			}
			if done {
				if err := s.tr.StopAggregation(); err != nil {
					return schema.EndError, err
				}
				return drainQuiet(ctx, s)
			}
		}
	}
}

// drainQuiet handles records until none arrives for quietFor and
// returns targets_exited — unless the duration or a signal ends the run
// first, which then is the reason, since the drain was cut short. It
// keeps polling the loss counters, so a gap in the targets' last records
// still gets its drop event within a poll interval.
func drainQuiet(ctx context.Context, s *session) (string, error) {
	tick := time.NewTicker(liveCheckEvery)
	defer tick.Stop()
	quiet := time.NewTimer(quietFor)
	defer quiet.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctxReason(ctx), drainStopped(s)
		case batch, ok := <-s.tr.Records():
			if !ok {
				return schema.EndError, errors.New("record stream closed unexpectedly")
			}
			if err := s.handleAll(batch); err != nil {
				return schema.EndError, err
			}
			quiet.Reset(quietFor)
		case <-tick.C:
			if err := s.poll(); err != nil {
				return endReason(err)
			}
		case <-quiet.C:
			return schema.EndTargetsExited, nil
		}
	}
}

// drainStopped handles the records already in flight when a signal or
// the duration stopped the run — until none arrives for quietFor, at most
// stopDrainMax — so a stop does not cut off the targets' last syscalls,
// which a pattern session (never over by itself) would otherwise always
// lose. What arrives later is lost to Close, uncounted.
func drainStopped(s *session) error {
	quiet := time.NewTimer(quietFor)
	defer quiet.Stop()
	bound := time.NewTimer(stopDrainMax)
	defer bound.Stop()
	for {
		select {
		case batch, ok := <-s.tr.Records():
			if !ok {
				return nil
			}
			if err := s.handleAll(batch); err != nil {
				return err
			}
			quiet.Reset(quietFor)
		case <-quiet.C:
			return nil
		case <-bound.C:
			return nil
		}
	}
}

// poll is the loop's periodic work: the loss counters, the correlator's
// clock (Correlator.Tick: a socket waiting for a cross-host end leaves
// after its fuse window even on an idle stream), the late attaches'
// lines and stages (task lh4), the Java join's rings — so an idle
// stream's held events still leave, and a late-attached JVM's new ring
// is read — and the segment writer's clock — rotation and the disk
// watchdog, whose trip ends the run as disk_limit (endReason). The correlator ticks only while no batch is
// waiting: with a backlog the records, not the clock, are behind.
func (s *session) poll() error {
	if _, err := pollLosses(s.tr, s.c); err != nil {
		return err
	}
	if len(s.tr.Records()) == 0 {
		if err := s.c.Tick(); err != nil {
			return err
		}
	}
	s.late.collect(s.java, s.log)
	if err := s.java.poll(); err != nil {
		return err
	}
	return s.seg.tick()
}

// ctxReason is the end reason of a run whose context is done: its
// duration bound expired, or a signal cancelled it.
func ctxReason(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return schema.EndDuration
	}
	return schema.EndStop
}

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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	stopProfile, err := startCPUProfile(cfg.cpuProfile)
	if err != nil {
		return err
	}
	return errors.Join(runTrace(ctx, cfg, stderr), stopProfile())
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
	Losses() (bpf.Losses, error)
	AddTarget(pid uint32) error
	RemoveTarget(pid uint32) error
	AdoptExec(pid uint32) (gap uint64, adopted bool, err error)
	IgnoreExec(pid uint32, since uint64) error
	StopAggregation() error
	PendingAggregates() ([]*types.RwAggregateEvent, error)
	StackTrace(id uint32) ([]uint64, error)
	AttachJavaMarks(pid uint32, natives []bpf.JavaNative) ([]string, error)
	Close() error
}

var _ tracer = (*bpf.Tracer)(nil)

// runTrace traces cfg.spec's targets until the duration ends, a signal
// arrives or (for PIDs and a port) every target has exited, then writes
// the event stream and the graph into cfg.out. The stream opens with a
// session header, records loss as drop events while it runs and closes
// with an end event naming why it stopped (PLAN.md, "Trace output
// contract").
//
// The tracer is opened before the output files are created, so a run that
// cannot attach (no root, no BTF) leaves an existing trace directory
// untouched instead of truncating its events next to an old graph. A
// stale ready file is removed first, so a harness reusing the path never
// mistakes a previous run's for this one's.
func runTrace(ctx context.Context, cfg runConfig, stderr io.Writer) error {
	if cfg.ready != "" {
		if err := os.Remove(cfg.ready); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("ready file: %w", err)
		}
	}
	pids, err := initialTargets(cfg.spec)
	if err != nil {
		return err
	}
	tr, err := bpf.Open(bpf.Config{Pids: pids, ExecWatch: cfg.spec.Mode() == target.ModePattern,
		Probes: bpf.ProbesFor(cfg.families), NoAggregation: cfg.noAggregation,
		NativeStacks: cfg.nativeStacks, JavaMarks: cfg.spool != ""})
	if err != nil {
		return err
	}
	out, err := openOutputs(cfg, tr, stderr)
	if err != nil {
		return errors.Join(err, tr.Close())
	}
	c, err := newCorrelator(ctx, cfg, out)
	if err != nil {
		return errors.Join(err, out.close(), tr.Close())
	}
	s := newSession(tr, c, cfg)
	s.java = out.java
	if err := s.start(pids); err != nil {
		return errors.Join(err, finish(cfg, tr, c, out, schema.EndError, stderr))
	}
	// The rings of JVMs already running get their natives probed before
	// the ready file promises the tracer is live (the Java mark, task td4).
	if err := s.java.poll(); err != nil {
		return errors.Join(err, finish(cfg, tr, c, out, schema.EndError, stderr))
	}
	_, _ = fmt.Fprintf(stderr, "iormesh: attached, tracing %s (%d tracepoints missing on this kernel)\n",
		describe(cfg.spec, pids), len(tr.Skipped()))
	if err := announceReady(cfg); err != nil {
		return errors.Join(err, finish(cfg, tr, c, out, schema.EndError, stderr))
	}
	reason, loopErr := traceLoop(ctx, s, cfg)
	return errors.Join(loopErr, finish(cfg, tr, c, out, reason, stderr))
}

// outputs are a run's optional tables beside the events file: the native
// stack table (-native-stacks) and the Java join's (-spool); each nil
// when its switch is off.
type outputs struct {
	stacks *stackOutput
	java   *javaOutput
}

func openOutputs(cfg runConfig, tr tracer, stderr io.Writer) (outputs, error) {
	st, err := openStacks(cfg, tr)
	if err != nil {
		return outputs{}, err
	}
	jo, err := openJava(cfg, tr, stderr)
	if err != nil {
		return outputs{}, errors.Join(err, st.close())
	}
	return outputs{stacks: st, java: jo}, nil
}

func (o outputs) close() error {
	return errors.Join(o.stacks.close(), o.java.close())
}

func (o outputs) summary() string {
	return o.stacks.summary() + o.java.summary()
}

// initialTargets resolves the processes target_map starts with: the
// PIDs, or the owners of the listening port (resolved once, at start —
// decision #23 leaves following the port across restarts to M6). A
// pattern starts empty; its matches are added after the probes attached
// (session.start), so none can exec unseen in between.
func initialTargets(spec target.Spec) ([]uint32, error) {
	if spec.Mode() == target.ModePattern {
		return nil, nil
	}
	return spec.Resolve(target.HostProcFS(), uint32(os.Getpid()))
}

// describe names the targets for the attach line.
func describe(spec target.Spec, pids []uint32) string {
	switch spec.Mode() {
	case target.ModePattern:
		return fmt.Sprintf("pattern %q", spec.Pattern)
	case target.ModeListenPort:
		return fmt.Sprintf("port %d owners, pid %v", spec.ListenPort, pids)
	}
	return fmt.Sprintf("pid %v", pids)
}

// finish ends a trace: a last loss poll (the counters die with the
// tracer), the tracer's close, the end event, the sink's close (the Java
// join's last ring read with it), the graph file and the optional tables'
// close, then the one-line summary. A loss count it cannot read
// turns the end reason into error: the trace might hide a gap.
func finish(cfg runConfig, tr tracer, c *correlate.Correlator, out outputs, reason string,
	stderr io.Writer) error {
	aggErr := drainAggregates(tr, c)
	losses, lossErr := pollLosses(tr, c)
	if lossErr != nil || aggErr != nil {
		reason = schema.EndError
	}
	closeErr := tr.Close()
	endErr := c.End(reason)
	sinkErr := c.Close()
	graphErr := schema.WriteGraphFile(filepath.Join(cfg.out, GraphFile), c.Graph())
	tablesErr := out.close()
	// Skipped runs are host-wide possible loss, reported beside the lost
	// records and never in them (bpf.Losses.Missed, task vd4).
	_, _ = fmt.Fprintf(stderr, "iormesh: stopped (%s); lost records %d (kernel %d), skipped runs %d, "+
		"unpaired exits %d, unreadable paths %d, unattributed aggregates %d%s\n",
		reason, losses.Total(), losses.Kernel, losses.Missed, c.Unpaired(), c.NoPath(), c.Unattributed(),
		out.summary())
	return errors.Join(aggErr, lossErr, closeErr, endErr, sinkErr, graphErr, tablesErr)
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

// newCorrelator describes the host, creates cfg.out and the correlator
// writing its events file there — through the Java join with -spool —
// and queues the session header. out.stacks is the native stack table,
// nil without -native-stacks.
func newCorrelator(ctx context.Context, cfg runConfig, out outputs) (*correlate.Correlator, error) {
	host, err := machineID()
	if err != nil {
		return nil, err
	}
	session, err := hostSession(ctx)
	if err != nil {
		return nil, err
	}
	offset, err := correlate.BootToWallOffset()
	if err != nil {
		return nil, fmt.Errorf("boot clock: %w", err)
	}
	if err := os.MkdirAll(cfg.out, 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(filepath.Join(cfg.out, EventsFile))
	if err != nil {
		return nil, err
	}
	c := correlate.New(correlate.Config{Host: correlate.Host{ID: host, Project: cfg.project},
		Sink: out.java.sink(schema.NewNDJSONSink(f)), WallOffsetNS: offset,
		Stacks: out.stacks.ids()})
	if err := c.Session(session); err != nil {
		return nil, errors.Join(err, c.Close())
	}
	return c, nil
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
				return schema.EndError, err
			}
			if s.over() {
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
				return schema.EndError, err
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

// poll is the loop's periodic work: the loss counters, and the Java
// join's rings, so an idle stream's held events still leave.
func (s *session) poll() error {
	if _, err := pollLosses(s.tr, s.c); err != nil {
		return err
	}
	return s.java.poll()
}

// ctxReason is the end reason of a run whose context is done: its
// duration bound expired, or a signal cancelled it.
func ctxReason(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return schema.EndDuration
	}
	return schema.EndStop
}

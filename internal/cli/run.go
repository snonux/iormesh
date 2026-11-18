package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"iormesh/internal/bpf"
	"iormesh/internal/correlate"
	"iormesh/internal/schema"
)

// liveCheckEvery is how often run checks whether its targets still exist;
// quietFor is how long it keeps draining once they are all gone, so the
// records of their last syscalls are not lost.
const (
	liveCheckEvery = 200 * time.Millisecond
	quietFor       = 300 * time.Millisecond
)

func runCommand(args []string, stderr io.Writer) error {
	cfg, err := parseRunFlags(args, stderr)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return runTrace(ctx, cfg, stderr)
}

// tracer is what run needs of bpf.Tracer; tests substitute a fake to
// drive the loop without a kernel.
type tracer interface {
	Records() <-chan any
	Losses() (bpf.Losses, error)
	Close() error
}

var _ tracer = (*bpf.Tracer)(nil)

// runTrace traces cfg.pids until the duration ends, a signal arrives or
// every target has exited, then writes the event stream and the graph
// into cfg.out. The stream opens with a session header, records loss as
// drop events while it runs and closes with an end event naming why it
// stopped (PLAN.md, "Trace output contract").
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
	tr, err := bpf.Open(bpf.Config{Pids: cfg.pids})
	if err != nil {
		return err
	}
	c, err := newCorrelator(ctx, cfg)
	if err != nil {
		return errors.Join(err, tr.Close())
	}
	_, _ = fmt.Fprintf(stderr, "iormesh: attached, tracing pid %v (%d tracepoints missing on this kernel)\n",
		cfg.pids, len(tr.Skipped()))
	if err := announceReady(cfg); err != nil {
		return errors.Join(err, finish(cfg, tr, c, schema.EndError, stderr))
	}
	reason, loopErr := traceLoop(ctx, tr, c, cfg)
	return errors.Join(loopErr, finish(cfg, tr, c, reason, stderr))
}

// finish ends a trace: a last loss poll (the counters die with the
// tracer), the tracer's close, the end event, the sink's close and the
// graph file, then the one-line summary. A loss count it cannot read
// turns the end reason into error: the trace might hide a gap.
func finish(cfg runConfig, tr tracer, c *correlate.Correlator, reason string, stderr io.Writer) error {
	losses, lossErr := pollLosses(tr, c)
	if lossErr != nil {
		reason = schema.EndError
	}
	closeErr := tr.Close()
	endErr := c.End(reason)
	sinkErr := c.Close()
	graphErr := schema.WriteGraphFile(filepath.Join(cfg.out, GraphFile), c.Graph())
	_, _ = fmt.Fprintf(stderr, "iormesh: stopped (%s); lost records %d (kernel %d), unpaired exits %d, unreadable paths %d\n",
		reason, losses.Total(), losses.Kernel, c.Unpaired(), c.NoPath())
	return errors.Join(lossErr, closeErr, endErr, sinkErr, graphErr)
}

// newCorrelator describes the host, creates cfg.out and the correlator
// writing its events file there, and queues the session header.
func newCorrelator(ctx context.Context, cfg runConfig) (*correlate.Correlator, error) {
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
		Sink: schema.NewNDJSONSink(f), WallOffsetNS: offset})
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

// pollLosses turns growth of the tracer's loss counters into a drop event,
// so a gap is visible in the stream close to where it happened.
func pollLosses(tr tracer, c *correlate.Correlator) (bpf.Losses, error) {
	l, err := tr.Losses()
	if err != nil {
		return l, fmt.Errorf("loss counters: %w", err)
	}
	return l, c.RecordLosses(l.Total())
}

// traceLoop feeds records to the correlator until a stop condition and
// returns why it stopped, as a schema end reason. A sink error stops the
// trace at once: tracing on into a broken file would only hide the loss.
// So does an unreadable loss counter, since drops must never be silent.
func traceLoop(ctx context.Context, tr tracer, c *correlate.Correlator, cfg runConfig) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.duration)
	defer cancel()
	tick := time.NewTicker(liveCheckEvery)
	defer tick.Stop()
	for {
		select {
		case rec, ok := <-tr.Records():
			if !ok {
				// Only Close ends the stream, and run has not closed it.
				return schema.EndError, errors.New("record stream closed unexpectedly")
			}
			if err := c.Handle(rec); err != nil {
				return schema.EndError, err
			}
		case <-ctx.Done():
			return ctxReason(ctx), nil
		case <-tick.C:
			if _, err := pollLosses(tr, c); err != nil {
				return schema.EndError, err
			}
			if !anyAlive(cfg.pids) {
				return drainQuiet(ctx, tr, c)
			}
		}
	}
}

// drainQuiet handles records until none arrives for quietFor and
// returns targets_exited — unless the duration or a signal ends the run
// first, which then is the reason, since the drain was cut short. It
// keeps polling the loss counters, so a gap in the targets' last records
// still gets its drop event within a poll interval.
func drainQuiet(ctx context.Context, tr tracer, c *correlate.Correlator) (string, error) {
	tick := time.NewTicker(liveCheckEvery)
	defer tick.Stop()
	quiet := time.NewTimer(quietFor)
	defer quiet.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctxReason(ctx), nil
		case rec, ok := <-tr.Records():
			if !ok {
				return schema.EndError, errors.New("record stream closed unexpectedly")
			}
			if err := c.Handle(rec); err != nil {
				return schema.EndError, err
			}
			quiet.Reset(quietFor)
		case <-tick.C:
			if _, err := pollLosses(tr, c); err != nil {
				return schema.EndError, err
			}
		case <-quiet.C:
			return schema.EndTargetsExited, nil
		}
	}
}

// ctxReason is the end reason of a run whose context is done: its
// duration bound expired, or a signal cancelled it.
func ctxReason(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return schema.EndDuration
	}
	return schema.EndStop
}

func anyAlive(pids []uint32) bool {
	for _, pid := range pids {
		if processExists(pid) {
			return true
		}
	}
	return false
}

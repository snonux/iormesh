package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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

// runTrace traces cfg.pids until the duration ends, a signal arrives or
// every target has exited, then writes the event stream and the graph
// into cfg.out.
func runTrace(ctx context.Context, cfg runConfig, stderr io.Writer) error {
	c, err := newCorrelator(cfg)
	if err != nil {
		return err
	}
	tr, err := bpf.Open(bpf.Config{Pids: cfg.pids})
	if err != nil {
		return errors.Join(err, c.Close())
	}
	if err := announceReady(cfg, tr, stderr); err != nil {
		return errors.Join(err, tr.Close(), c.Close())
	}
	reason, loopErr := traceLoop(ctx, tr, c, cfg)
	losses, lossErr := tr.Losses()
	closeErr := tr.Close()
	sinkErr := c.Close()
	graphErr := schema.WriteGraphFile(filepath.Join(cfg.out, GraphFile), c.Graph())
	_, _ = fmt.Fprintf(stderr, "iormesh: stopped (%s); lost records %d (kernel %d), unpaired exits %d, unreadable paths %d\n",
		reason, losses.Total(), losses.Kernel, c.Unpaired(), c.NoPath())
	return errors.Join(loopErr, lossErr, closeErr, sinkErr, graphErr)
}

// newCorrelator creates cfg.out and the correlator writing its events
// file there.
func newCorrelator(cfg runConfig) (*correlate.Correlator, error) {
	host, err := machineID()
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
	return correlate.New(correlate.Config{Host: correlate.Host{ID: host, Project: cfg.project},
		Sink: schema.NewNDJSONSink(f), WallOffsetNS: offset}), nil
}

// announceReady reports the attached probe set and creates cfg.ready.
func announceReady(cfg runConfig, tr *bpf.Tracer, stderr io.Writer) error {
	_, _ = fmt.Fprintf(stderr, "iormesh: attached, tracing pid %v (%d tracepoints missing on this kernel)\n",
		cfg.pids, len(tr.Skipped()))
	if cfg.ready == "" {
		return nil
	}
	f, err := os.Create(cfg.ready)
	if err != nil {
		return fmt.Errorf("ready file: %w", err)
	}
	return f.Close()
}

// machineID is the host id every event carries (/etc/machine-id).
func machineID() (string, error) {
	b, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return "", fmt.Errorf("host id: %w", err)
	}
	id := strings.TrimSpace(string(b))
	if id == "" {
		return "", errors.New("host id: /etc/machine-id is empty")
	}
	return id, nil
}

// traceLoop feeds records to the correlator until a stop condition and
// returns why it stopped. A sink error stops the trace at once: tracing
// on into a broken file would only hide the loss.
func traceLoop(ctx context.Context, tr *bpf.Tracer, c *correlate.Correlator, cfg runConfig) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.duration)
	defer cancel()
	tick := time.NewTicker(liveCheckEvery)
	defer tick.Stop()
	for {
		select {
		case rec, ok := <-tr.Records():
			if !ok {
				return "record stream closed", nil
			}
			if err := c.Handle(rec); err != nil {
				return "write error", err
			}
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "duration", nil
			}
			return "signal", nil
		case <-tick.C:
			if !anyAlive(cfg.pids) {
				return "targets exited", drainQuiet(tr, c)
			}
		}
	}
}

// drainQuiet handles records until none arrives for quietFor.
func drainQuiet(tr *bpf.Tracer, c *correlate.Correlator) error {
	for {
		select {
		case rec, ok := <-tr.Records():
			if !ok {
				return nil
			}
			if err := c.Handle(rec); err != nil {
				return err
			}
		case <-time.After(quietFor):
			return nil
		}
	}
}

func anyAlive(pids []uint32) bool {
	for _, pid := range pids {
		if processExists(pid) {
			return true
		}
	}
	return false
}

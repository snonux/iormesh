package cli

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"iormesh/internal/schema"
	"iormesh/internal/segment"
)

// defaultTraceDir is where a server session's parquet segments go when
// run is given no -out (PLAN.md M4 "Disk watchdog": -trace-dir, default
// /opt/iormesh/traces). With -out alone a run writes NDJSON only, as
// before M4, so the integration suite and local runs never touch /opt.
const defaultTraceDir = "/opt/iormesh/traces"

// segmentFlags are run's M4 output flags, registered by addSegmentFlags
// and folded into the runConfig by apply.
type segmentFlags struct {
	traceDir, minFree, maxBytes, owner *string
	limit                              *float64
}

// segOutput is a run's parquet segment writer (-trace-dir); nil writes
// none, and its methods are no-ops then, as stackOutput's are.
// dir, session and fqdn name the project directory and the session the
// run's state.json (state.go) describes.
type segOutput struct {
	w                  *segment.Writer
	stderr             io.Writer
	dir, session, fqdn string
}

// segmentSink feeds the correlator's events to the segment writer. Its
// Close does not close the writer: the stack table may still write rows
// after the event stream closed, so the writer closes last, with the
// tables (outputs.close).
type segmentSink struct{ w *segment.Writer }

// teeSink writes every event to both sinks: -out's NDJSON and
// -trace-dir's segments in one run.
type teeSink struct{ a, b schema.Sink }

func addSegmentFlags(fs *flag.FlagSet) segmentFlags {
	return segmentFlags{
		traceDir: fs.String("trace-dir", "",
			"write parquet segments to DIR/<project>/ (default "+defaultTraceDir+" when -out is not given)"),
		limit: fs.Float64("disk-limit", segment.DefaultMaxUsedPercent,
			"stop tracing when the trace dir's partition is this percent used (0: off)"),
		minFree: fs.String("disk-min-free", strconv.Itoa(segment.DefaultMinFreeBytes>>20)+"MiB",
			"stop tracing when the trace dir's partition has less free (KiB, MiB, GiB suffixes; 0: off)"),
		maxBytes: fs.String("disk-max-bytes", "0",
			"stop tracing when the trace dir itself holds this much (in a pod: below the emptyDir sizeLimit; 0: off)"),
		owner: fs.String("ready-owner", "",
			"UID:GID to own DIR/<project>/ready, so that user can collect segments (start passes SUDO_UID:SUDO_GID)"),
	}
}

// apply validates the segment flags into cfg. A run without -out writes
// segments to the default trace dir: some output is always written.
func (f segmentFlags) apply(cfg runConfig) (runConfig, error) {
	cfg.traceDir = *f.traceDir
	if cfg.traceDir == "" && cfg.out == "" {
		cfg.traceDir = defaultTraceDir
	}
	// Written as the accepted range negated, so NaN (which compares false
	// both ways and would silently turn the limit off) is rejected too.
	if !(*f.limit >= 0 && *f.limit <= 100) {
		return cfg, fmt.Errorf("%w: -disk-limit %g: want 0-100", errUsage, *f.limit)
	}
	cfg.diskLimit = *f.limit
	var err error
	if cfg.diskMinFree, err = parseSize(*f.minFree); err != nil {
		return cfg, fmt.Errorf("%w: -disk-min-free: %v", errUsage, err)
	}
	if cfg.diskMaxBytes, err = parseSize(*f.maxBytes); err != nil {
		return cfg, fmt.Errorf("%w: -disk-max-bytes: %v", errUsage, err)
	}
	if *f.owner != "" {
		if cfg.readyOwner, err = parseOwner(*f.owner); err != nil {
			return cfg, fmt.Errorf("%w: -ready-owner: %v", errUsage, err)
		}
	}
	return cfg, nil
}

// parseSize reads a byte count with an optional binary suffix.
func parseSize(s string) (uint64, error) {
	mult := uint64(1)
	for _, u := range []struct {
		suffix string
		mult   uint64
	}{{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}} {
		if num, ok := strings.CutSuffix(s, u.suffix); ok {
			s, mult = num, u.mult
			break
		}
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n > (1<<63)/mult {
		return 0, fmt.Errorf("%q: want a byte count like 256MiB", s)
	}
	return n * mult, nil
}

// parseOwner reads UID:GID, numeric: the tracer runs as root under
// systemd, where name lookups would add a dependency for nothing.
func parseOwner(s string) (*segment.Owner, error) {
	u, g, ok := strings.Cut(s, ":")
	uid, uerr := strconv.ParseUint(u, 10, 31)
	gid, gerr := strconv.ParseUint(g, 10, 31)
	if !ok || uerr != nil || gerr != nil {
		return nil, fmt.Errorf("%q: want numeric UID:GID", s)
	}
	return &segment.Owner{UID: int(uid), GID: int(gid)}, nil
}

// watchdog is the run's disk watchdog over the configured trace dir (the
// partition the segments fill), nil without -trace-dir.
func (cfg runConfig) watchdog() *segment.Watchdog {
	if cfg.traceDir == "" {
		return nil
	}
	return &segment.Watchdog{Dir: cfg.traceDir, MaxUsedPercent: cfg.diskLimit,
		MinFreeBytes: cfg.diskMinFree, MaxDirBytes: cfg.diskMaxBytes}
}

// checkTraceDisk is the startup check (PLAN.md: "fails fast if the
// partition is already at the limit"): it runs before the BPF object is
// loaded, so a full disk costs milliseconds, not the seconds of a load.
// The trace dir is created here, since statfs needs a path that exists;
// segment.MakeDirs never chmods a directory that already exists.
func checkTraceDisk(cfg runConfig) error {
	wd := cfg.watchdog()
	if wd == nil {
		return nil
	}
	if err := segment.MakeDirs(cfg.traceDir); err != nil {
		return fmt.Errorf("trace dir: %w", err)
	}
	return wd.Check()
}

// openSegments opens the segment writer for cfg's project, named by the
// host's FQDN and a fresh session id; nil without -trace-dir.
func openSegments(cfg runConfig, fqdn string, stderr io.Writer) (*segOutput, error) {
	if cfg.traceDir == "" {
		return nil, nil
	}
	id, err := newSessionID()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(cfg.traceDir, cfg.project)
	w, err := segment.New(segment.Config{Dir: dir,
		FQDN: fqdn, Session: id, Project: cfg.project, Owner: cfg.readyOwner,
		Watchdog: cfg.watchdog()})
	if err != nil {
		return nil, err
	}
	return &segOutput{w: w, stderr: stderr, dir: dir, session: id, fqdn: fqdn}, nil
}

// newSessionID is <session> in segment names: 8 random hex digits, so two
// runs of one project on one host never share a name even when started
// in the same second.
func newSessionID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("session id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// sink puts the segment writer beside next (the NDJSON sink, nil
// without -out).
func (o *segOutput) sink(next schema.Sink) schema.Sink {
	switch {
	case o == nil:
		return next
	case next == nil:
		return segmentSink{o.w}
	}
	return teeSink{next, segmentSink{o.w}}
}

// tick drives rotation and the disk watchdog from the run loop's ticker.
// A disk-limit trip is reported here with its numbers; the loop turns it
// into end reason disk_limit (endReason).
func (o *segOutput) tick() error {
	if o == nil {
		return nil
	}
	err := o.w.Tick(time.Now())
	if errors.Is(err, segment.ErrDiskLimit) {
		_, _ = fmt.Fprintf(o.stderr, "iormesh: %v; stopping\n", err)
	}
	return err
}

// close publishes the last segment.
func (o *segOutput) close() error {
	if o == nil {
		return nil
	}
	return o.w.Close()
}

// The table hooks: each forwards a row to the writer, nothing without
// -trace-dir.
func (o *segOutput) method(m schema.Method) error {
	if o == nil {
		return nil
	}
	return o.w.Method(m)
}

func (o *segOutput) thread(t schema.Thread) error {
	if o == nil {
		return nil
	}
	return o.w.Thread(t)
}

func (o *segOutput) class(c schema.Class) error {
	if o == nil {
		return nil
	}
	return o.w.Class(c)
}

func (o *segOutput) stack(s schema.Stack) error {
	if o == nil {
		return nil
	}
	return o.w.Stack(s)
}

func (s segmentSink) Event(ev *schema.Event) error { return s.w.Event(ev) }
func (s segmentSink) Close() error                 { return nil }

func (t teeSink) Event(ev *schema.Event) error {
	if err := t.a.Event(ev); err != nil {
		return err
	}
	return t.b.Event(ev)
}

func (t teeSink) Close() error { return errors.Join(t.a.Close(), t.b.Close()) }

// endReason maps a poll error to the run's end: a disk-limit trip is a
// clean end (PLAN.md: flush, record the reason, exit 0), anything else an
// error.
func endReason(err error) (string, error) {
	if errors.Is(err, segment.ErrDiskLimit) {
		return schema.EndDiskLimit, nil
	}
	return schema.EndError, err
}

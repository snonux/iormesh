package query

import (
	"errors"
	"fmt"
	"math"
	"path"
	"strconv"
	"strings"
	"time"

	"iormesh/internal/schema"
)

// clockLayout is a time of day, seconds with an optional fraction
// ("12:00:05", "12:00:05.250").
const clockLayout = "15:04:05.999999999"

// ErrInvalid marks a filter Validate (or ParseTime) rejected: a usage
// error, reported before any segment is read.
var ErrInvalid = errors.New("invalid filter")

// Filter is one slice. Every set field must match (they AND); a zero
// field matches every event. The globs are path.Match patterns: '*'
// stops at '/', so "/etc/*" is the files directly in /etc.
type Filter struct {
	// From and To bound the event's wall_ns: From ≤ wall_ns < To.
	// From 0 is unbounded; To bounds only with HasTo, so that a -to of
	// the epoch itself (0) is an empty window, not an open one. Each
	// event is judged by its own host's clock, as recorded and not
	// corrected for the skew between hosts — the merge's clock bounds
	// order cross-host instants but do not rewrite them (PLAN.md M4
	// "Clock reconciliation").
	From, To uint64
	HasTo    bool
	// Host matches the store's host directory (the registry's host
	// name) or the session's FQDN.
	Host string
	// App matches the process's app (the target pattern of the session
	// that traced it, as the merged graph's app node names it) or its
	// comm.
	App string
	// Thread matches the Java thread name (java.thread) or the kernel
	// tid in decimal.
	Thread string
	// Conn is a connection id of the merged trace, matched exactly:
	// both ends of a fused connection carry it.
	Conn string
	// Follow widens Conn to the threads that used the connection: on
	// each host, every event of a thread with an event on Conn, from
	// that thread's first to its last event on it — the file and other
	// I/O a request caused on the way through.
	Follow bool
	// File matches args.path or args.new_path; the fd a successful
	// open, openat, openat2 or creat of a matching path returned
	// matches too (its reads, writes, close), until it is closed.
	// Only that fd instance of the opening process is followed: a dup
	// of it, or the copy a forked child inherits, is another fd
	// instance and is not. The trace records no process exit, so an
	// fd its process never closed stays tracked to the end (it counts
	// toward maxTracked); an exec keeps it, as the kernel keeps an fd
	// without O_CLOEXEC.
	File string
	// Method matches a Java frame of the event's stack, by
	// "class#method" or by the full frame text
	// "class#method(file:line)".
	Method string
	// Ref names one event, as a conversation step of the build's view
	// model carries it (viewmodel.Step.Ref): "<session>/<seq>", the
	// session as the store names it ("<project>/<host>/<fqdn>-<id>",
	// Source.Session) and the event's seq. It is how `show sequence`
	// and the site turn a step into its raw event (docs/visualization.md
	// "Detail on demand").
	Ref string
	// Event is one event kind (schema.Kinds), matched exactly.
	Event string
	// PID matches the event's pid when HasPID is set (a pid of 0 is
	// a value, not "unset", as with HasTo).
	PID    uint32
	HasPID bool
	// Errno matches the event's err: the errno name the tracer recorded
	// for a failed syscall (ret < 0). It is the recorded spelling —
	// ParseErrno turns a number, a lowercase name or an alias into it.
	Errno string
	// OtelTrace matches otel.trace_id: the events that ran inside a
	// span of that OpenTelemetry trace (Java-attributed only).
	OtelTrace string
}

// Validate rejects a filter no event could be judged by: a malformed
// glob, Follow without Conn, an empty or reversed time window, an
// unknown event kind or errno name, a malformed trace id.
func (f Filter) Validate() error {
	if err := f.validateGlobsAndRef(); err != nil {
		return err
	}
	if err := f.validateValues(); err != nil {
		return err
	}
	switch {
	case f.Follow && f.Conn == "":
		return fmt.Errorf("%w: -follow needs -conn", ErrInvalid)
	case f.HasTo && f.From >= f.To:
		return fmt.Errorf("%w: the window -from %d -to %d is empty", ErrInvalid, f.From, f.To)
	}
	return nil
}

// validateValues checks the exact-value filters (task wh4): an unknown
// kind is refused with the list of known ones, an errno must be a name
// the tracer records (ParseErrno's output), a trace id the W3C form
// events carry.
func (f Filter) validateValues() error {
	if f.Event != "" && !schema.KnownKind(f.Event) {
		return fmt.Errorf("%w: unknown event kind %q; the kinds are: %s",
			ErrInvalid, f.Event, strings.Join(schema.Kinds(), ", "))
	}
	if f.Errno != "" && !knownErrno(f.Errno) {
		return fmt.Errorf("%w: -errno %q is no errno name the tracer records", ErrInvalid, f.Errno)
	}
	if f.OtelTrace != "" && !schema.ValidTraceID(f.OtelTrace) {
		return fmt.Errorf("%w: -otel-trace %q: want 32 lowercase hex digits, not all zero",
			ErrInvalid, f.OtelTrace)
	}
	return nil
}

// validateGlobsAndRef checks the glob patterns and the ref.
func (f Filter) validateGlobsAndRef() error {
	for _, g := range []struct{ name, pattern string }{{"host", f.Host}, {"app", f.App},
		{"thread", f.Thread}, {"file", f.File}, {"method", f.Method}} {
		if _, err := path.Match(g.pattern, ""); err != nil {
			return fmt.Errorf("%w: -%s %q: %v", ErrInvalid, g.name, g.pattern, err)
		}
	}
	if f.Ref != "" {
		if _, _, err := ParseRef(f.Ref); err != nil {
			return err
		}
	}
	return nil
}

// ParseRef splits a Ref into its session and seq. The seq is what
// follows the last '/': a session key holds slashes of its own.
func ParseRef(ref string) (session string, seq uint64, err error) {
	i := strings.LastIndexByte(ref, '/')
	if i > 0 {
		seq, err = strconv.ParseUint(ref[i+1:], 10, 64)
	}
	if i <= 0 || err != nil || seq == 0 {
		return "", 0, fmt.Errorf("%w: -ref %q: want <session>/<seq> (a step's ref)", ErrInvalid, ref)
	}
	return ref[:i], seq, nil
}

// MatchesMethod reports whether a method table row is one Method names.
func (f Filter) MatchesMethod(m *schema.Method) bool {
	return f.Method != "" && (glob(f.Method, m.Class+"#"+m.Method) || glob(f.Method, m.Frame))
}

// TimeSpec is a -from or -to value: an absolute instant (RFC 3339), or
// a time of day (UTC) that Resolve places on the trace's first day.
type TimeSpec struct {
	abs   time.Time
	clock time.Duration
	// isClock marks a time of day.
	isClock bool
}

// ParseTime parses an RFC 3339 instant ("2026-11-20T12:00:05Z") or a
// UTC time of day ("12:00:05", "12:00:05.5"). An instant outside
// 1970–2262 is rejected: wall_ns (an int64 by the reader's bound)
// cannot hold it.
func ParseTime(s string) (TimeSpec, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		if t.Before(time.Unix(0, 0)) || t.After(time.Unix(0, math.MaxInt64)) {
			return TimeSpec{}, fmt.Errorf("%w: time %q is outside 1970–2262", ErrInvalid, s)
		}
		return TimeSpec{abs: t}, nil
	}
	c, err := time.Parse(clockLayout, s)
	if err != nil {
		return TimeSpec{}, fmt.Errorf("%w: time %q: want RFC 3339 or HH:MM:SS[.frac] (UTC)",
			ErrInvalid, s)
	}
	midnight := time.Date(c.Year(), c.Month(), c.Day(), 0, 0, 0, 0, time.UTC)
	return TimeSpec{clock: c.Sub(midnight), isClock: true}, nil
}

// Resolve is the spec's wall_ns. A time of day is taken on day's UTC
// date — ctl passes the start of the earliest events segment listed
// for the projects (federate.FirstStart, for build and query alike), so
// "12:00:05" means 12:00:05 of the day the trace began; a window across
// midnight needs RFC 3339. A day whose date wall_ns cannot hold (a
// segment name says year 9999) is an error, not a wrapped instant.
func (t TimeSpec) Resolve(day time.Time) (uint64, error) {
	if !t.isClock {
		return uint64(t.abs.UnixNano()), nil
	}
	d := day.UTC()
	at := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC).Add(t.clock)
	if at.Before(time.Unix(0, 0)) || at.After(time.Unix(0, math.MaxInt64)) {
		return 0, fmt.Errorf("time of day on %s is outside 1970–2262", d.Format(time.DateOnly))
	}
	return uint64(at.UnixNano()), nil
}

// glob is path.Match for a pattern Validate accepted.
func glob(pattern, s string) bool {
	ok, _ := path.Match(pattern, s) // Validate rejected a bad pattern
	return ok
}

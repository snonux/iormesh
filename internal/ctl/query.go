package ctl

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"iormesh/internal/federate"
	"iormesh/internal/query"
	"iormesh/internal/schema"
	"iormesh/internal/segment"
	"iormesh/internal/termsafe"
)

// `iormeshctl query` (PLAN.md M5 "Query / slice interface", task pb4):
// cut the merged trace of the collected segments into a size-capped
// slice of schema NDJSON (internal/query).
//
// It reads the store exactly as build does, and trusts it no more: every
// segment is decoded first in the memory-capped child (check.go), each
// host directory is held to its pinned identity, and a segment that
// fails is left out — named on stderr, the exit status 1 — never half
// read. With -from/-to only the segments whose range overlaps the
// window are loaded (task ik4: loadSelection, build's selection and its
// guarantee, window.go), and the slice is computed from them alone. The
// merge runs again so that the slice speaks the merged
// connection ids (both ends of a fused connection share one) and knows
// each process's app. -method reads the sessions' methods tables, each
// checked in the child the same way. Unlike build, query writes nothing:
// no database import, no new host pin, no build output.
//
// The store, not the last build's model, is the input: the model is a
// bounded summary and holds no event (docs/visualization.md), the
// segments hold all of them. Predicate pushdown onto the parquet columns
// (docs/implementation.md M5) would skip decoding rows a filter rejects,
// but every row is decoded in the child anyway before the controller
// may read the file, so the filters run on decoded events.

const (
	// defaultMaxBytes caps a slice when -max-bytes is not given: about
	// a quarter million tokens, what an LLM's context can take whole.
	defaultMaxBytes = 1 << 20
	// maxMethodIDs bounds the method ids -method may select across all
	// sessions: the tables come from servers.
	maxMethodIDs = 1 << 20
)

// queryFlags are query's flags.
type queryFlags struct {
	paths                              buildFlags
	from, to                           string
	host, app, thread, conn, file, mth string
	ref                                string
	event, pid, errno, otelTrace       string
	follow                             bool
	maxBytes                           int64
}

// queryCommand parses query's flags, checks the filter before reading
// anything, and runs the query.
func queryCommand(args []string, e env) error {
	fs := newFlagSet("query", e.stderr)
	var f queryFlags
	addQueryFlags(fs, &f)
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: query takes no arguments", errUsage)
	}
	filter, err := parseQueryFilter(&f)
	if err != nil {
		return err
	}
	return runQuery(e.root(), e, f, filter)
}

// addQueryFlags registers query's flags on fs into f.
func addQueryFlags(fs *flag.FlagSet, f *queryFlags) {
	fs.Var(&f.paths.projects, "project", "query this project (repeatable; default: every project in the store)")
	fs.StringVar(&f.paths.store, "store", "", "local trace store (default ~/.local/share/iormesh/traces)")
	fs.StringVar(&f.paths.db, "db", "", "service database; the host pins next to it are honored "+
		"(default ~/.local/share/iormesh/services.db)")
	fs.StringVar(&f.from, "from", "", "first instant, RFC 3339 or HH:MM:SS[.frac] UTC on the trace's first day")
	fs.StringVar(&f.to, "to", "", "end instant (exclusive), as -from")
	fs.StringVar(&f.host, "host", "", "host glob: store host name or FQDN")
	fs.StringVar(&f.app, "app", "", "app glob: the app's target pattern or the process's comm")
	fs.StringVar(&f.thread, "thread", "", "thread glob: Java thread name or kernel tid")
	fs.StringVar(&f.conn, "conn", "", "connection id of the merged trace")
	fs.BoolVar(&f.follow, "follow", false, "with -conn: also the threads' other events while they used it")
	fs.StringVar(&f.file, "file", "", "path glob: events naming it and the fds opened from it")
	fs.StringVar(&f.mth, "method", "", "Java frame glob: class#method or class#method(file:line)")
	fs.StringVar(&f.ref, "ref", "", "one event, by a conversation step's ref (<session>/<seq>; iormeshctl show sequence)")
	fs.StringVar(&f.event, "event", "", "event kind, exactly (openat, connect, http, ...)")
	fs.StringVar(&f.pid, "pid", "", "process id, in decimal")
	fs.StringVar(&f.errno, "errno", "", "failed with this errno: a name (ENOENT) or a number (2)")
	fs.StringVar(&f.otelTrace, "otel-trace", "", "OpenTelemetry trace id (32 hex digits): events inside its spans")
	fs.Int64Var(&f.maxBytes, "max-bytes", defaultMaxBytes, "output cap in bytes, the truncation record included")
}

// parseQueryFilter turns the flags into a filter and validates it, the
// time window aside: -from and -to are parsed into f.paths.window as
// build's are (parseWindow) and placed only once the store is listed — a
// time of day needs the trace's first day (windowSpec.resolve).
func parseQueryFilter(f *queryFlags) (query.Filter, error) {
	filter := query.Filter{Host: f.host, App: f.app, Thread: f.thread, Conn: f.conn,
		Follow: f.follow, File: f.file, Method: f.mth, Ref: f.ref, Event: f.event}
	if err := parseValueFlags(*f, &filter); err != nil {
		return query.Filter{}, err
	}
	if err := parseWindow(windowFlags{from: f.from, to: f.to}, &f.paths); err != nil {
		return query.Filter{}, err
	}
	if err := filter.Validate(); err != nil {
		return query.Filter{}, fmt.Errorf("%w: %w", errUsage, err)
	}
	if f.maxBytes < query.MinMaxBytes() {
		return query.Filter{}, fmt.Errorf("%w: -max-bytes %d is below %d, the truncation record's size",
			errUsage, f.maxBytes, query.MinMaxBytes())
	}
	return filter, nil
}

// parseValueFlags puts -pid, -errno and -otel-trace into filter (task
// wh4): the pid as a decimal uint32, the errno as the name events carry
// (query.ParseErrno), the trace id lowercased — the form events carry,
// so one copied from an OTEL backend in capitals still matches. The
// filter's Validate checks the rest, -event's kind included.
func parseValueFlags(f queryFlags, filter *query.Filter) error {
	if f.pid != "" {
		pid, err := strconv.ParseUint(f.pid, 10, 32)
		if err != nil {
			return fmt.Errorf("%w: -pid %q: want a decimal process id", errUsage, f.pid)
		}
		filter.PID, filter.HasPID = uint32(pid), true
	}
	if f.errno != "" {
		name, err := query.ParseErrno(f.errno)
		if err != nil {
			return fmt.Errorf("%w: %w", errUsage, err)
		}
		filter.Errno = name
	}
	filter.OtelTrace = strings.ToLower(f.otelTrace)
	return nil
}

// runQuery loads the store — with -from or -to only the segments whose
// range overlaps the window (loadSelection, build's selection; task
// ik4) — merges it, and writes the slice; bad segments are reported
// after it.
func runQuery(ctx context.Context, e env, f queryFlags, filter query.Filter) error {
	paths, err := resolveBuildPaths(e, f.paths)
	if err != nil {
		return err
	}
	sel, _, err := loadSelection(ctx, e, f.paths, paths)
	if err != nil {
		return err
	}
	segs := sel.segs
	switch {
	case len(segs.Sources) > 0:
	case sel.window != nil && len(segs.Bad) == 0:
		// No segment overlaps the window, so no event lies in it:
		// the empty slice a whole-store query gave before.
		return nil
	default:
		return fmt.Errorf("query: no readable segment for %s%s in %s (%d bad)",
			strings.Join(sel.projects, ", "), windowNote(sel.window), paths.store, len(segs.Bad))
	}
	windowFilter(&filter, sel.window)
	in, err := queryInput(ctx, &segs, filter)
	if err != nil {
		return err
	}
	// The bad segments are reported even when the query fails: what
	// it wrote is still missing them.
	_, err = query.Run(ctx, e.stdout, in, filter, f.maxBytes)
	return errors.Join(err, reportBad(e, segs.Bad))
}

// windowFilter cuts the events to the resolved window (nil: no window)
// by their wall_ns, From ≤ wall_ns < To, a side not given left open:
// the window selected the segments, whole, and they hold events of its
// edges' neighbours too.
func windowFilter(filter *query.Filter, w *federate.Window) {
	if w == nil {
		return
	}
	filter.From = wallNS(w.From)
	filter.To, filter.HasTo = wallNS(w.To), !w.To.IsZero()
}

// queryInput merges the segments and, for -method, reads the method
// ids the filter selects; a methods table that fails its check joins
// segs.Bad.
func queryInput(ctx context.Context, segs *federate.Segments, filter query.Filter) (query.Input, error) {
	sources := make([]federate.Source, len(segs.Sources))
	in := query.Input{Sources: make([]query.Source, len(segs.Sources))}
	for i, s := range segs.Sources {
		sources[i] = s
		in.Sources[i] = query.Source{Host: s.Host, FQDN: s.File.FQDN, Session: s.Session(), Events: s.Events}
	}
	res, err := federate.Merge(ctx, sources, federate.Options{})
	if err != nil {
		return query.Input{}, err
	}
	in.Renames, in.Graph = res.Renames, res.Graph
	if filter.Method != "" {
		in.Methods, err = methodIDs(ctx, segs, filter)
	}
	return in, err
}

// reportBad names the segments left out on stderr and fails the
// command: a slice missing part of the trace must not pass for whole.
func reportBad(e env, bad []federate.BadSegment) error {
	for _, b := range bad {
		_, _ = fmt.Fprintf(e.stderr, "iormeshctl: query: left out %s: %s\n",
			termsafe.String(b.Name), errText(b.Err))
	}
	if len(bad) > 0 {
		return fmt.Errorf("%w: %d bad segments left out of the query (listed above)",
			errBadSegments, len(bad))
	}
	return nil
}

// methodIDs reads the methods tables of the queried sessions and keeps,
// per session, the ids whose frame the filter's -method matches. Each
// table is decoded in the memory-capped child first and its footer held
// to its name and place; one that fails is added to segs.Bad and its
// ids are missing — the exit status says so.
func methodIDs(ctx context.Context, segs *federate.Segments, filter query.Filter) (map[string]map[uint64]bool, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	check := childChecker(exe, checkAddressSpace)
	sessions := map[string]bool{}
	for _, s := range segs.Sources {
		sessions[s.Session()] = true
	}
	out := map[string]map[uint64]bool{}
	n := 0
	for _, t := range segs.TableFiles {
		if t.File.Table != segment.TableMethods || !sessions[t.Session()] {
			continue
		}
		if err := readMethods(ctx, check, t, filter, out, &n); err != nil {
			if ctx.Err() != nil || errors.Is(err, errTooManyMethods) {
				return nil, err
			}
			segs.Bad = append(segs.Bad, federate.BadSegment{Name: t.Name(), Err: err})
		}
	}
	return out, nil
}

// errTooManyMethods stops a -method query whose matches exceed
// maxMethodIDs.
var errTooManyMethods = errors.New("query: -method matches too many frames: narrow the pattern")

// readMethods checks one methods table and adds its matching ids to
// out, counting them in n. The ids are kept only when the whole table
// read cleanly: a table that fails adds none, never half.
func readMethods(ctx context.Context, check federate.Checker, t federate.TableFile,
	filter query.Filter, out map[string]map[uint64]bool, n *int) error {
	found := map[uint64]bool{}
	err := readMethodTable(ctx, check, t, func(m *schema.Method) error {
		if filter.MatchesMethod(m) && !found[m.MethodID] && !out[t.Session()][m.MethodID] {
			if *n+len(found) >= maxMethodIDs {
				return errTooManyMethods
			}
			found[m.MethodID] = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	if out[t.Session()] == nil {
		out[t.Session()] = map[uint64]bool{}
	}
	for id := range found {
		out[t.Session()][id] = true
	}
	*n += len(found)
	return nil
}

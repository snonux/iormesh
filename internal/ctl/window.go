package ctl

import (
	"flag"
	"fmt"
	"time"

	"iormesh/internal/federate"
	"iormesh/internal/query"
)

// build's time window (task yh4): `iormeshctl build -from T -to T` or
// `-last DUR` builds only the segments whose time range overlaps the
// window (federate.Window: by their file names, so a segment outside it
// is never decoded once its host directory is pinned). The window
// selects whole segments, not events: the build covers the window
// widened to its segments' edges, about a minute or two each side. T is
// query's (query.ParseTime): RFC 3339, or a UTC time of day placed on
// the day of the earliest segment of the projects; -last counts back
// from now. build echoes the resolved window. The service database's
// views (services, edges, request counts) are read back narrowed to the
// window too (selection.scope): the database keeps every import ever
// made.
//
// A windowed build is another build than the full one, so it never
// replaces it: it writes <projects>[@<namespace>]~window/ ('~' is in
// neither the project nor the namespace alphabet), one directory per
// project set and namespace that each windowed build overwrites, and
// `show -windowed`, `open -windowed` and `tui -windowed` read it (task
// hk4). The resolved window is recorded in the model as Run.Window, the
// text build echoes, so the site and the TUI can say which window they
// show; a full build's model has none and is unchanged by it.

// windowSuffix ends a windowed build's directory name (buildKey).
const windowSuffix = "~window"

// windowSpec is the parsed window flags: -from and -to (nil when not
// given) or -last (0 when not given).
type windowSpec struct {
	from, to *query.TimeSpec
	last     time.Duration
}

// windowFlags are build's raw window flags, parsed by parseWindow. -last
// is a string, not a flag.Duration, so that "-last 0s" (an error) is
// told from no -last at all.
type windowFlags struct {
	from, to, last string
}

// addWindowFlags registers -from, -to and -last on build's flag set
// (not refresh's: refresh builds everything it collected).
func addWindowFlags(fs *flag.FlagSet, w *windowFlags) {
	fs.StringVar(&w.from, "from", "", "build only the segments from this instant on: RFC 3339, "+
		"or HH:MM:SS[.frac] UTC on the trace's first day; into <projects>~window")
	fs.StringVar(&w.to, "to", "", "build only the segments before this instant (exclusive), as -from")
	fs.StringVar(&w.last, "last", "", "build only the segments of the last DUR (e.g. 2h), "+
		"not with -from or -to")
}

// parseWindow checks the window flags before anything is read and puts
// them into f; f.windowed is set when there is a window.
func parseWindow(w windowFlags, f *buildFlags) error {
	if w.last != "" {
		if w.from != "" || w.to != "" {
			return fmt.Errorf("%w: -last is a window of its own: give it or -from/-to, not both", errUsage)
		}
		d, err := time.ParseDuration(w.last)
		if err != nil || d <= 0 {
			return fmt.Errorf("%w: -last %s: want a positive duration (90m, 2h)", errUsage, safe(w.last))
		}
		f.window.last = d
	}
	for _, t := range []struct {
		s   string
		dst **query.TimeSpec
	}{{w.from, &f.window.from}, {w.to, &f.window.to}} {
		if t.s == "" {
			continue
		}
		spec, err := query.ParseTime(t.s)
		if err != nil {
			return fmt.Errorf("%w: %w", errUsage, err)
		}
		*t.dst = &spec
	}
	f.windowed = f.window.last > 0 || f.window.from != nil || f.window.to != nil
	return nil
}

// resolve turns the window into the instants federate selects by, nil
// without one: -last from the clock, a time of day on the day of the
// earliest segment of projects in store. An empty window is a usage
// error.
func (w windowSpec) resolve(e env, store string, projects []string) (*federate.Window, error) {
	if w.last > 0 {
		return &federate.Window{From: e.clock().Add(-w.last).UTC().Truncate(time.Second)}, nil
	}
	if w.from == nil && w.to == nil {
		return nil, nil
	}
	day, err := federate.FirstStart(store, projects)
	if err != nil {
		return nil, err
	}
	var out federate.Window
	for _, t := range []struct {
		spec *query.TimeSpec
		dst  *time.Time
	}{{w.from, &out.From}, {w.to, &out.To}} {
		if t.spec == nil {
			continue
		}
		ns, err := t.spec.Resolve(day)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errUsage, err)
		}
		*t.dst = time.Unix(0, int64(ns)).UTC()
	}
	if w.from != nil && w.to != nil && !out.From.Before(out.To) {
		return nil, fmt.Errorf("%w: the window -from %s -to %s is empty", errUsage,
			safe(out.From.Format(time.RFC3339Nano)), safe(out.To.Format(time.RFC3339Nano)))
	}
	return &out, nil
}

// windowText is the resolved window as build echoes it, "FROM .. TO"
// in RFC 3339 UTC, "(open)" for a side not given.
func windowText(w *federate.Window) string {
	side := func(t time.Time) string {
		if t.IsZero() {
			return "(open)"
		}
		return t.UTC().Format(time.RFC3339Nano)
	}
	return side(w.From) + " .. " + side(w.To)
}

// windowNote is " in the window FROM .. TO" for a windowed build's
// messages, "" without a window.
func windowNote(w *federate.Window) string {
	if w == nil {
		return ""
	}
	return " in the window " + windowText(w)
}

// key is the build directory's name for projects under f: buildKey,
// with windowSuffix for a windowed build (or a reader's -windowed). It
// is the one key build writes and show, open and tui read.
func (f buildFlags) key(projects []string) string {
	k := buildKey(projects, f.namespace)
	if f.windowed {
		k += windowSuffix
	}
	return k
}

// name names f's build of projects in messages: buildName, with
// windowSuffix for a windowed one.
func (f buildFlags) name(projects []string) string {
	n := buildName(projects, f.namespace)
	if f.windowed {
		n += windowSuffix
	}
	return n
}

// rebuildArgs are the build flags that write f's build, for a hint:
// namespaceArg, and a window's flags (any window writes the same
// directory).
func (f buildFlags) rebuildArgs() string {
	args := namespaceArg(f.namespace)
	if f.windowed {
		args += " -from T|-to T|-last DUR"
	}
	return args
}

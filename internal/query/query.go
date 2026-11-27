package query

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"iormesh/internal/federate"
	"iormesh/internal/schema"
	"iormesh/internal/termsafe"
)

// Source is one checked segment as the query reads it.
type Source struct {
	// Host is the store's host directory, FQDN the session's.
	Host, FQDN string
	// Session scopes the segment's method ids (Input.Methods).
	Session string
	// Events streams the segment's events in seq order; fn borrows the
	// event for the call (schema.Sink's rule).
	Events func(ctx context.Context, fn func(*schema.Event) error) error
}

// Input is what a query reads: the sources in order and what the merge
// and the method tables contributed.
type Input struct {
	Sources []Source
	// Renames turns each host's local conn ids into the merged trace's
	// (federate.Result.Renames): a slice speaks the merged ids, so both
	// ends of a fused connection answer to one -conn.
	Renames map[federate.LocalConn]string
	// Graph is the merged graph; App reads its app → process edges.
	Graph schema.Graph
	// Methods holds, per Source.Session, the method ids whose frame
	// Filter.MatchesMethod accepted.
	Methods map[string]map[uint64]bool
	// Frames holds, per Source.Session, the frame text of each method
	// id ("class#method(file:line)"), for -frames (decision #42, task
	// an4). Nil leaves the lines schema events; non-nil, even empty,
	// ends every line with a Java stack in java_frames.
	Frames map[string]map[uint64]string
}

// Stats is what a query wrote.
type Stats struct {
	// Emitted counts the event lines written, Remaining the matching
	// events cut by the cap.
	Emitted, Remaining uint64
	// Truncated says the last line written was the truncation record.
	Truncated bool
	// FileBaseMatch is set when File matched no event's path but the
	// last element of one: the first such path. A path.Match '*' stops
	// at '/', so "*billing.crt*" names no absolute path; the caller
	// tells the user so instead of leaving an empty slice unexplained
	// (decision #41, task 0n4). The path comes from a server.
	FileBaseMatch string
}

// Run writes the events of in that f selects to w as NDJSON, at most
// maxBytes of it (≥ MinMaxBytes) including the truncation record a cut
// ends in. With Follow it reads the sources twice: once to find the
// connection's threads, once to write. A source error stops the query;
// what was written stays written, without a truncation record — the
// caller's error is the signal.
func Run(ctx context.Context, w io.Writer, in Input, f Filter, maxBytes int64) (Stats, error) {
	if err := f.Validate(); err != nil {
		return Stats{}, err
	}
	if maxBytes < MinMaxBytes() {
		return Stats{}, fmt.Errorf("%w: -max-bytes %d is below %d, the truncation record's size",
			ErrInvalid, maxBytes, MinMaxBytes())
	}
	m := newMatcher(f, in)
	if f.Follow {
		var err error
		if m.follow, err = followThreads(ctx, in, f.Conn); err != nil {
			return Stats{}, err
		}
	}
	out := newCappedWriter(w, maxBytes)
	var buf []byte
	err := each(ctx, in, func(src *Source, ev *schema.Event) error {
		ok, err := m.match(src, ev)
		if err != nil || !ok {
			return err
		}
		buf = schema.AppendEvent(buf[:0], ev)
		if in.Frames != nil && ev.Java != nil && len(ev.Java.Stack) > 0 {
			buf = appendFrames(buf, in.Frames[src.Session], ev.Java.Stack)
		}
		return out.line(termsafe.JSON(append(buf, '\n')))
	})
	if err != nil {
		return out.stats, err
	}
	st, err := out.close()
	st.FileBaseMatch = m.fileBaseMatch()
	return st, err
}

// appendFrames ends line, one encoded event object, with java_frames:
// the frame text of each id of stack, in its order (innermost first),
// from the session's frames; an id they lack is "method#<id>", as the
// view model names one (viewmodel.Recorder.Frame). The member is
// appended after the event's own, not into its java object, so the
// rest of the line is byte for byte the event's schema encoding and
// dropping the member gives back a strict schema event (decision #42).
// A frame comes from a server: termsafe.JSON escapes the whole line
// after.
func appendFrames(line []byte, frames map[uint64]string, stack []uint64) []byte {
	line = append(line[:len(line)-1], `,"java_frames":[`...)
	for i, id := range stack {
		if i > 0 {
			line = append(line, ',')
		}
		f, ok := frames[id]
		if !ok {
			f = "method#" + strconv.FormatUint(id, 10)
		}
		quoted, _ := json.Marshal(f) // a string always encodes
		line = append(line, quoted...)
	}
	return append(line, "]}"...)
}

// each calls fn for every event of every source, in order, with the
// event's conn id already renamed to the merged trace's.
func each(ctx context.Context, in Input, fn func(*Source, *schema.Event) error) error {
	for i := range in.Sources {
		src := &in.Sources[i]
		err := src.Events(ctx, func(ev *schema.Event) error {
			if ev.ConnID != "" {
				if id, ok := in.Renames[federate.LocalConn{Host: ev.HostID, ConnID: ev.ConnID}]; ok {
					ev.ConnID = id
				}
			}
			return fn(src, ev)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// followThreads is Follow's first pass: every thread with an event on
// conn, and the wall-clock span of those events. Host-level records
// (pid 0) belong to no thread.
func followThreads(ctx context.Context, in Input, conn string) (map[threadKey]span, error) {
	out := map[threadKey]span{}
	err := each(ctx, in, func(_ *Source, ev *schema.Event) error {
		if ev.ConnID != conn || ev.PID == 0 {
			return nil
		}
		k := threadOf(ev)
		s, ok := out[k]
		switch {
		case !ok && len(out) >= maxTracked:
			return fmt.Errorf("query: over %d threads on -conn %q: narrow the filter", maxTracked, conn)
		case !ok:
			s = span{first: ev.WallNS, last: ev.WallNS}
		}
		s.first, s.last = min(s.first, ev.WallNS), max(s.last, ev.WallNS)
		out[k] = s
		return nil
	})
	return out, err
}

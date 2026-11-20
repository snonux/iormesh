package federate

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"iormesh/internal/project"
	"iormesh/internal/schema"
	"iormesh/internal/segment"
)

// Segment is one collected events segment in the controller's store,
// <store>/<project>/<host>/<fqdn>-<session>-<start-ts>-<seq>.parquet
// (collect's layout, PLAN.md M4 "Collect"), as a merge Source.
//
// Each segment is its own source, not each session: the service
// database counts a connection's requests per source (servicedb
// Request.Source), and only a segment is immutable once collected —
// a session grows with every collect, and the operator may delete its
// old segments, so per-segment counts are the ones that stay exact
// (task hb4). The graph, though, is a session's: the server keeps none
// (decision #3), so LoadSegments rebuilds it from all of the session's
// segments (rebuild.go) and every segment of the session returns it.
type Segment struct {
	// Path is the events file.
	Path string
	// Project and Host are the store directories the file sits in: the
	// project label and the registry's host name.
	Project, Host string
	File          segment.FileName
	sess          *session
}

var _ Source = (*Segment)(nil)

// session is one tracer run's segments on one host, with the graph the
// load pass rebuilt from them and the host_id their events carry.
type session struct {
	hostID string
	graph  schema.Graph
}

// BadSegment is a segment the build skipped, and why.
type BadSegment struct {
	Name string
	Err  error
}

// Segments is what LoadSegments found in the store.
type Segments struct {
	// Sources are the segments that read cleanly, sorted by Name.
	Sources []*Segment
	// Bad are the events segments that did not: unreadable, a footer that
	// disagrees with the name or the directory, an event that does not
	// validate. They are skipped whole, never half-read.
	Bad []BadSegment
	// Ignored are store entries that are no segment file (dot files,
	// rsync's in-flight temporaries, are skipped silently).
	Ignored []string
	// Tables counts the table files seen (methods, threads, classes,
	// stacks); the build does not read them yet (M5's conversations do).
	Tables int
}

// Name is the segment's path below the store, <project>/<host>/<file>:
// stable wherever the store lives, so the service database's
// per-source counts survive a moved store, and unique, since collect
// gives every host its own directory.
func (s *Segment) Name() string {
	return s.Project + "/" + s.Host + "/" + filepath.Base(s.Path)
}

// Graph is the session's graph, rebuilt from its events by LoadSegments.
func (s *Segment) Graph(_ context.Context) (schema.Graph, error) {
	if s.sess == nil {
		return schema.Graph{}, fmt.Errorf("federate: %s: segment not loaded", s.Name())
	}
	return s.sess.graph, nil
}

// Events streams the segment's events (segment.ReadEvents, strict),
// checking each against its directory and session.
func (s *Segment) Events(ctx context.Context, fn func(*schema.Event) error) error {
	return s.read(ctx, func(ev *schema.Event) error {
		if s.sess != nil && ev.HostID != s.sess.hostID {
			return fmt.Errorf("federate: %s: host_id %q, the session's is %q",
				s.Name(), ev.HostID, s.sess.hostID)
		}
		return fn(ev)
	})
}

// read streams the events after checking the footer agrees with the
// file's name and directory: a segment renamed or copied into another
// project's or host's directory would otherwise merge as something it is
// not.
func (s *Segment) read(ctx context.Context, fn func(*schema.Event) error) error {
	meta, err := segment.ReadMeta(s.Path)
	if err != nil {
		return err
	}
	f := s.File
	if meta.Project != s.Project || meta.FQDN != f.FQDN || meta.Session != f.Session || meta.Seq != f.Seq {
		return fmt.Errorf("federate: %s: footer names %s/%s-%s-%06d", s.Name(),
			meta.Project, meta.FQDN, meta.Session, meta.Seq)
	}
	_, err = segment.ReadEvents(ctx, s.Path, func(ev *schema.Event) error {
		if ev.Project != s.Project {
			return fmt.Errorf("federate: %s: event seq %d of project %q", s.Name(), ev.Seq, ev.Project)
		}
		return fn(ev)
	})
	return err
}

// LoadSegments lists the events segments of projects (every project
// directory when none is given) in store and reads each once: it checks
// every event and rebuilds each session's graph from them. A segment
// that fails is reported in Bad and left out — its session's graph is
// rebuilt without it — so the merge that follows never reads half a
// file. A requested project without a store directory is an error; a
// cancelled ctx is returned as is.
func LoadSegments(ctx context.Context, store string, projects []string) (Segments, error) {
	var out Segments
	segs, err := listStore(store, projects, &out)
	if err != nil {
		return Segments{}, err
	}
	for _, group := range groupSessions(segs) {
		good, bad, err := loadSession(ctx, group)
		if err != nil {
			return Segments{}, err
		}
		out.Sources = append(out.Sources, good...)
		out.Bad = append(out.Bad, bad...)
	}
	slices.SortFunc(out.Sources, func(a, b *Segment) int { return cmp.Compare(a.Name(), b.Name()) })
	slices.SortFunc(out.Bad, func(a, b BadSegment) int { return cmp.Compare(a.Name, b.Name) })
	return out, nil
}

// loadSession reads a session's segments in seq order into one graph.
// A segment that fails is dropped and the session read again without
// it: the graph's open-fd state runs across segments, so a half-read
// segment's effects cannot be taken back otherwise. Damage is rare, so
// the re-read costs nothing in the common case.
func loadSession(ctx context.Context, segs []*Segment) ([]*Segment, []BadSegment, error) {
	var bad []BadSegment
	for {
		sess := &session{}
		g := newGraphRebuilder()
		failed := -1
		for i, s := range segs {
			err := s.read(ctx, func(ev *schema.Event) error {
				if sess.hostID == "" {
					sess.hostID = ev.HostID
				} else if ev.HostID != sess.hostID {
					return fmt.Errorf("federate: %s: host_id %q, the session's is %q",
						s.Name(), ev.HostID, sess.hostID)
				}
				g.add(ev)
				return nil
			})
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, nil, ctxErr
			}
			if err != nil {
				bad, failed = append(bad, BadSegment{Name: s.Name(), Err: err}), i
				break
			}
		}
		if failed < 0 {
			sess.graph = g.graph()
			for _, s := range segs {
				s.sess = sess
			}
			return segs, bad, nil
		}
		segs = slices.Delete(slices.Clone(segs), failed, failed+1)
	}
}

// groupSessions groups segments by session (project, host directory,
// fqdn, session id), each group in seq order, the groups in name order.
func groupSessions(segs []*Segment) [][]*Segment {
	key := func(s *Segment) string {
		return s.Project + "/" + s.Host + "/" + s.File.FQDN + "-" + s.File.Session
	}
	slices.SortFunc(segs, func(a, b *Segment) int {
		return cmp.Or(cmp.Compare(key(a), key(b)), cmp.Compare(a.File.Seq, b.File.Seq))
	})
	var out [][]*Segment
	for i, s := range segs {
		if i == 0 || key(segs[i-1]) != key(s) {
			out = append(out, nil)
		}
		out[len(out)-1] = append(out[len(out)-1], s)
	}
	return out
}

// listStore finds the events segments under store/<project>/<host>/,
// counting table files and noting stray entries in out.
func listStore(store string, projects []string, out *Segments) ([]*Segment, error) {
	if len(projects) == 0 {
		var err error
		if projects, err = subdirs(store, project.Valid); err != nil {
			return nil, err
		}
	}
	var segs []*Segment
	for _, p := range projects {
		if !project.Valid(p) {
			return nil, fmt.Errorf("federate: invalid project %q", p)
		}
		hosts, err := subdirs(filepath.Join(store, p), ValidHost)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("federate: nothing collected for project %s in %s", p, store)
		}
		if err != nil {
			return nil, err
		}
		for _, h := range hosts {
			found, err := listHost(filepath.Join(store, p, h), p, h, out)
			if err != nil {
				return nil, err
			}
			segs = append(segs, found...)
		}
	}
	return segs, nil
}

// listHost lists one host directory's events segments.
func listHost(dir, proj, host string, out *Segments) ([]*Segment, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("federate: %w", err)
	}
	var segs []*Segment
	for _, en := range entries {
		name := en.Name()
		if strings.HasPrefix(name, ".") {
			continue // rsync's in-flight temporaries
		}
		f, err := segment.ParseName(name)
		if err != nil || !en.Type().IsRegular() {
			out.Ignored = append(out.Ignored, filepath.Join(proj, host, name))
			continue
		}
		if f.Table != "" {
			out.Tables++
			continue
		}
		segs = append(segs, &Segment{Path: filepath.Join(dir, name), Project: proj, Host: host, File: f})
	}
	return segs, nil
}

// subdirs lists dir's subdirectories whose name passes valid, sorted;
// anything else is not the store's and is left alone.
func subdirs(dir string, valid func(string) bool) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("federate: %w", err)
	}
	var out []string
	for _, en := range entries {
		if en.IsDir() && valid(en.Name()) {
			out = append(out, en.Name())
		}
	}
	return out, nil
}

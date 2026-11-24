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
// (collect's layout, docs/milestones.md M4 "Collect"), as a merge Source.
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
	// hostID is the host id the check found in the segment's events.
	hostID string
	sess   *session
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
	// rsync's in-flight temporaries, and a pod's container map,
	// ContainersFile, are skipped silently).
	Ignored []string
	// Tables counts the table files seen (methods, threads, classes,
	// stacks); the build does not read them yet (M5's conversations do).
	Tables int
	// TableFiles are those table files, sorted by Name, unchecked:
	// iormeshctl query -method reads the methods tables of the
	// sessions in Sources, each checked first like a segment.
	TableFiles []TableFile
	// Hosts is the identity each host directory's good sessions carry:
	// what the caller pins (LoadOptions.Known) for the next build.
	Hosts map[string]HostIdentity
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
// checking each against its directory and session — its pod object
// against the session's pod FQDN too (checkPod, task sh4).
func (s *Segment) Events(ctx context.Context, fn func(*schema.Event) error) error {
	return s.read(ctx, func(ev *schema.Event) error {
		if s.sess != nil && ev.HostID != s.sess.hostID {
			return fmt.Errorf("federate: %s: host_id %q, the session's is %q",
				s.Name(), ev.HostID, s.sess.hostID)
		}
		if err := checkPod(ev, s.File.FQDN); err != nil {
			return fmt.Errorf("federate: %s: %w", s.Name(), err)
		}
		return fn(ev)
	})
}

// read streams the events after checking the footer agrees with the
// file's name and directory (wantMeta): a segment renamed or copied into
// another project's or host's directory would otherwise merge as
// something it is not.
func (s *Segment) read(ctx context.Context, fn func(*schema.Event) error) error {
	_, err := segment.ReadEvents(ctx, s.Path, s.wantMeta, func(ev *schema.Event) error {
		if ev.Project != s.Project {
			return fmt.Errorf("federate: %s: event seq %d of project %q", s.Name(), ev.Seq, ev.Project)
		}
		return fn(ev)
	})
	return err
}

// Session names the tracer run the segment belongs to,
// <project>/<host>/<fqdn>-<session>: the scope of its method-table ids.
func (s *Segment) Session() string { return sessionKey(s.Project, s.Host, s.File) }

// wantMeta checks a footer against the file's name and directory.
func (s *Segment) wantMeta(m segment.Meta) error { return wantMeta(s.Project, s.File, m) }

// TableFile is one collected table file of a segment (methods, threads,
// classes, stacks), in the same store layout as its events file.
type TableFile struct {
	Path          string
	Project, Host string
	File          segment.FileName
}

// Name is the file's path below the store, as Segment.Name.
func (t TableFile) Name() string {
	return t.Project + "/" + t.Host + "/" + filepath.Base(t.Path)
}

// Session is the run the table belongs to, as Segment.Session.
func (t TableFile) Session() string { return sessionKey(t.Project, t.Host, t.File) }

// WantMeta checks the table's footer against its name and directory, as
// a segment's is checked: a table moved into another session's place
// would otherwise lend that session its ids.
func (t TableFile) WantMeta(m segment.Meta) error { return wantMeta(t.Project, t.File, m) }

// sessionKey is the store-relative name of a session.
func sessionKey(project, host string, f segment.FileName) string {
	return project + "/" + host + "/" + f.FQDN + "-" + f.Session
}

// wantMeta checks a footer against what the file's name and project
// directory say.
func wantMeta(project string, f segment.FileName, m segment.Meta) error {
	if m.Project != project || m.FQDN != f.FQDN || m.Session != f.Session || m.Seq != f.Seq {
		return fmt.Errorf("footer names %s/%s-%s-%06d", m.Project, m.FQDN, m.Session, m.Seq)
	}
	return nil
}

// Checker decodes one events segment whole and summarizes it
// (segment.CheckEvents). The controller runs it in a memory-capped child
// process, so a hostile file that would exhaust memory fails that
// segment, not the build; nil checks in process.
type Checker func(ctx context.Context, path string) (segment.Summary, error)

// LoadOptions tune LoadSegments.
type LoadOptions struct {
	// Check decodes each segment before anything else reads it; nil is
	// segment.CheckEvents in this process.
	Check Checker
	// Known pins host directories (registry host names) to the identity
	// earlier builds saw there; a session claiming another is bad.
	Known map[string]HostIdentity
	// Window, if set, keeps only the segments that may hold events
	// inside it (window.go, task yh4). Segments outside it are not
	// decoded, except those of a host directory Known does not pin yet,
	// which the host vetting needs whole (Window.toCheck).
	Window *Window
}

// LoadSegments lists the events segments of projects (every project
// directory when none is given) in store, checks each once (opt.Check:
// every row decoded and validated, the footer matching the file's name
// and place), vets the hosts the sessions claim to be (vetHosts), and
// then rebuilds each session's graph from the segments that passed. A
// segment that fails is reported in Bad and left out, so the merge that
// follows never reads half a file. A requested project without a store
// directory is an error; a cancelled ctx is returned as is.
//
// With opt.Window only the window's segments are returned, each
// session's graph rebuilt from those alone; a segment of an unpinned
// directory is still checked, and reported in Bad when it fails, though
// it lies outside the window (Window.toCheck).
func LoadSegments(ctx context.Context, store string, projects []string, opt LoadOptions) (Segments, error) {
	var out Segments
	segs, err := listStore(store, projects, &out)
	if err != nil {
		return Segments{}, err
	}
	checked, err := checkAll(ctx, opt.Window.toCheck(segs, opt.Known), opt.Check, &out)
	if err != nil {
		return Segments{}, err
	}
	sessions := opt.Window.narrow(vetHosts(groupSessions(checked), opt.Known, &out))
	for _, group := range sessions {
		good, err := loadSession(ctx, group, &out)
		if err != nil {
			return Segments{}, err
		}
		out.Sources = append(out.Sources, good...)
	}
	slices.SortFunc(out.Sources, func(a, b *Segment) int { return cmp.Compare(a.Name(), b.Name()) })
	slices.SortFunc(out.Bad, func(a, b BadSegment) int { return cmp.Compare(a.Name, b.Name) })
	slices.SortFunc(out.TableFiles, func(a, b TableFile) int { return cmp.Compare(a.Name(), b.Name()) })
	return out, nil
}

// checkAll runs check on every segment and keeps the ones that pass,
// each with the host id its events carry; the rest go to out.Bad.
func checkAll(ctx context.Context, segs []*Segment, check Checker, out *Segments) ([]*Segment, error) {
	if check == nil {
		check = func(ctx context.Context, path string) (segment.Summary, error) {
			return segment.CheckEvents(ctx, path, nil)
		}
	}
	var good []*Segment
	for _, s := range segs {
		sum, err := check(ctx, s.Path)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if err == nil {
			if err = s.wantMeta(sum.Meta); err != nil {
				err = fmt.Errorf("segment: %s: %w", s.Path, err)
			}
		}
		if err != nil {
			out.bad(s, err)
			continue
		}
		s.hostID = sum.HostID
		good = append(good, s)
	}
	return good, nil
}

// bad records s as skipped. The error names the file by its store name,
// not the absolute path the reader used: the model built from it is
// meant to be the same wherever the store lives.
func (o *Segments) bad(s *Segment, err error) {
	msg := strings.ReplaceAll(err.Error(), s.Path, s.Name())
	o.Bad = append(o.Bad, BadSegment{Name: s.Name(), Err: errors.New(msg)})
}

// loadSession reads a checked session's segments in seq order into one
// graph. The check decoded every one already, so a failure here means a
// file changed under the build, or events whose host id or pod object
// (checkPod) is not the session's: the segment is dropped and the session
// read again without it (the graph's open-fd state runs across
// segments, so half a segment cannot be taken back otherwise).
func loadSession(ctx context.Context, segs []*Segment, out *Segments) ([]*Segment, error) {
	for {
		sess := &session{hostID: segs[0].hostID}
		g := newGraphRebuilder()
		failed := -1
		for i, s := range segs {
			err := s.read(ctx, func(ev *schema.Event) error {
				if ev.HostID != sess.hostID {
					return fmt.Errorf("federate: %s: host_id %q, the session's is %q",
						s.Name(), ev.HostID, sess.hostID)
				}
				if err := checkPod(ev, s.File.FQDN); err != nil {
					return fmt.Errorf("federate: %s: %w", s.Name(), err)
				}
				g.add(ev)
				return nil
			})
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			if err != nil {
				out.bad(s, err)
				failed = i
				break
			}
		}
		if failed < 0 {
			sess.graph = g.graph()
			for _, s := range segs {
				s.sess = sess
			}
			return segs, nil
		}
		if segs = slices.Delete(slices.Clone(segs), failed, failed+1); len(segs) == 0 {
			return nil, nil
		}
	}
}

// groupSessions groups segments by session (project, host directory,
// fqdn, session id), each group in seq order, the groups in name order.
func groupSessions(segs []*Segment) [][]*Segment {
	key := (*Segment).Session
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

// listHost lists one host directory's events segments; its container
// map, if any, is the build's to read (ReadContainers).
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
		if name == ContainersFile {
			// A pod's container map, which the build reads apart (task
			// jk4) and reports there when it is no regular file.
			continue
		}
		f, err := segment.ParseName(name)
		if err != nil || !en.Type().IsRegular() {
			out.Ignored = append(out.Ignored, filepath.Join(proj, host, name))
			continue
		}
		if f.Table != "" {
			out.Tables++
			out.TableFiles = append(out.TableFiles,
				TableFile{Path: filepath.Join(dir, name), Project: proj, Host: host, File: f})
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

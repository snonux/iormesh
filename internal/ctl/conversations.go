package ctl

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"

	"iormesh/internal/federate"
	"iormesh/internal/schema"
	"iormesh/internal/segment"
	"iormesh/internal/viewmodel"
)

// The conversations of a build (viewmodel.Recorder; docs/visualization.md
// V2). Their http events are read along with servicedb.Observe's pass
// (recordedSegment), so they cost no read of their own; the file work of
// the threads that served the kept requests takes one more read of the
// segments, and the Java frames of the kept steps one read of each
// methods table they need — checked in the memory-capped child first,
// like every segment (check.go).

// recordedSegment is a segment whose http events also feed the
// recorder as Observe reads it.
type recordedSegment struct {
	*federate.Segment
	rec *viewmodel.Recorder
}

func (s recordedSegment) Events(ctx context.Context, fn func(*schema.Event) error) error {
	o := originOf(s.Segment)
	return s.Segment.Events(ctx, func(ev *schema.Event) error {
		s.rec.HTTP(o, ev)
		return fn(ev)
	})
}

// originOf is the recorder's name for a segment's session.
func originOf(s *federate.Segment) viewmodel.Origin {
	return viewmodel.Origin{Session: s.Session(), FQDN: s.File.FQDN}
}

// recordConversations plans the conversations from the http events
// Observe's pass recorded, reads the kept requests' file work and
// resolves their Java frames. A methods table that fails its check
// joins segs.Bad (the build then exits 1) and its frames stay bare ids.
func recordConversations(ctx context.Context, segs *federate.Segments, rec *viewmodel.Recorder,
	clocks []federate.ClockBound) error {
	rec.Plan(federate.NewTimeline(clocks))
	if rec.Wants() {
		for _, s := range segs.Sources {
			o := originOf(s)
			err := s.Events(ctx, func(ev *schema.Event) error {
				rec.File(o, ev)
				return nil
			})
			if err != nil {
				return err
			}
		}
	}
	return readFrames(ctx, segs, rec)
}

// readFrames names the method ids the kept steps' stacks hold, from the
// methods tables of their sessions.
func readFrames(ctx context.Context, segs *federate.Segments, rec *viewmodel.Recorder) error {
	want := rec.Methods()
	if len(want) == 0 {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	check := childChecker(exe, checkAddressSpace)
	for _, t := range segs.TableFiles {
		ids := want[t.Session()]
		// A table the rollup's read (loadMethodTables) already left out
		// is named once, not twice.
		if t.File.Table != segment.TableMethods || len(ids) == 0 || isBad(segs.Bad, t.Name()) {
			continue
		}
		if err := readFrameTable(ctx, check, t, ids, rec); err != nil {
			if ctx.Err() != nil {
				return err
			}
			segs.Bad = append(segs.Bad, federate.BadSegment{Name: t.Name(), Err: err})
		}
	}
	return nil
}

// readFrameTable checks one methods table and hands the recorder the
// frames of ids (sorted). Frames are handed over only when the whole
// table read cleanly — a table that fails adds none, never half.
func readFrameTable(ctx context.Context, check federate.Checker, t federate.TableFile,
	ids []uint64, rec *viewmodel.Recorder) error {
	sum, err := check(ctx, t.Path)
	if err == nil {
		err = t.WantMeta(sum.Meta)
	}
	found := map[uint64]string{}
	if err == nil {
		_, err = segment.ReadMethods(ctx, t.Path, t.WantMeta, func(m *schema.Method) error {
			if _, ok := slices.BinarySearch(ids, m.MethodID); ok {
				found[m.MethodID] = m.Frame
			}
			return nil
		})
	}
	if err != nil {
		return errors.New(strings.ReplaceAll(err.Error(), t.Path, t.Name()))
	}
	for _, id := range ids {
		if f, ok := found[id]; ok {
			rec.Frame(t.Session(), id, f)
		}
	}
	return nil
}

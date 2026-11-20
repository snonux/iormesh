package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"iormesh/internal/correlate"
	"iormesh/internal/schema"
	"iormesh/internal/stacks"
)

// procRoot is where the symbolizer reads /proc/<pid>/maps and the mapped
// files (through /proc/<pid>/root).
const procRoot = "/proc"

// stackOutput is a -native-stacks run's stack table and where it goes
// (M2 step 9): stacks.ndjson in -out, the segments' stacks tables with
// -trace-dir. A nil *stackOutput is a run without native stacks; its
// methods are no-ops then, so the run's code paths stay one.
type stackOutput struct {
	table *stacks.Table
	w     *schema.StackWriter // nil without -out
}

// openStacks creates the table that fills cfg.out/stacks.ndjson (with
// -out) and the segments' stacks tables (seg, nil without -trace-dir)
// from tr's stack map, or, without -native-stacks, removes a stale
// stacks.ndjson a previous run left in cfg.out: it would describe stack
// ids no event of this trace carries.
func openStacks(cfg runConfig, tr tracer, seg *segOutput) (*stackOutput, error) {
	path := filepath.Join(cfg.out, StacksFile)
	if !cfg.nativeStacks {
		if cfg.out == "" {
			return nil, nil
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("stale %s: %w", StacksFile, err)
		}
		return nil, nil
	}
	o := &stackOutput{}
	if cfg.out != "" {
		if err := os.MkdirAll(cfg.out, 0o755); err != nil {
			return nil, err
		}
		f, err := os.Create(path)
		if err != nil {
			return nil, err
		}
		o.w = schema.NewStackWriter(f)
	}
	o.table = stacks.NewTable(tr.StackTrace, procRoot, o.writer(seg))
	return o, nil
}

// writer is the table worker's write: each new stack to stacks.ndjson
// and to the segments, whichever the run has.
func (s *stackOutput) writer(seg *segOutput) func(schema.Stack) error {
	return func(st schema.Stack) error {
		if s.w != nil {
			if err := s.w.Stack(st); err != nil {
				return err
			}
		}
		return seg.stack(st)
	}
}

// ids is the correlator's view of the table; a true nil interface without
// native stacks (a nil *stacks.Table in an interface would not be nil).
func (s *stackOutput) ids() correlate.StackIDs {
	if s == nil {
		return nil
	}
	return s.table
}

// close waits for the table's last stacks to be written, then flushes and
// closes stacks.ndjson.
func (s *stackOutput) close() error {
	if s == nil {
		return nil
	}
	err := s.table.Close()
	if s.w != nil {
		err = errors.Join(err, s.w.Close())
	}
	return err
}

// summary is the stack part of the run's summary line: how many distinct
// stacks the table holds, how many of them came inline because the stack
// map refused them (bucket collisions), and how many syscalls got none,
// split by why (a collision whose inline walk failed too, a faulted walk,
// other), so a trace with missing stacks says what went wrong.
func (s *stackOutput) summary() string {
	if s == nil {
		return ""
	}
	st := s.table.Stats()
	return fmt.Sprintf(", native stacks %d (inline %d; uncaptured %d: collisions %d, faults %d, other %d)",
		st.Stacks, st.Inline, st.Uncaptured, st.Collisions, st.Faults, st.Other)
}

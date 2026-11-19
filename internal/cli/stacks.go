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

// stackOutput is a -native-stacks run's stack table and its file (M2
// step 9). A nil *stackOutput is a run without native stacks; its methods
// are no-ops then, so the run's code paths stay one.
type stackOutput struct {
	table *stacks.Table
	w     *schema.StackWriter
}

// openStacks creates cfg.out/stacks.ndjson and the table that fills it
// from tr's stack map, or, without -native-stacks, removes a stale
// stacks.ndjson a previous run left in cfg.out: it would describe stack
// ids no event of this trace carries.
func openStacks(cfg runConfig, tr tracer) (*stackOutput, error) {
	path := filepath.Join(cfg.out, StacksFile)
	if !cfg.nativeStacks {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("stale %s: %w", StacksFile, err)
		}
		return nil, nil
	}
	if err := os.MkdirAll(cfg.out, 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := schema.NewStackWriter(f)
	return &stackOutput{table: stacks.NewTable(tr.StackTrace, procRoot, w.Stack), w: w}, nil
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
	return errors.Join(s.table.Close(), s.w.Close())
}

// summary is the stack part of the run's summary line: how many distinct
// stacks the table holds and how many syscalls got none.
func (s *stackOutput) summary() string {
	if s == nil {
		return ""
	}
	st := s.table.Stats()
	return fmt.Sprintf(", native stacks %d (uncaptured %d)", st.Stacks, st.Uncaptured)
}

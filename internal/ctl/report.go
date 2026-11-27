package ctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"iormesh/internal/federate"
	"iormesh/internal/report"
	"iormesh/internal/schema"
	"iormesh/internal/segment"
	"iormesh/internal/viewmodel"
)

// The per-run summary rollup (docs/milestones.md M5, internal/report): build
// feeds a report.Collector the events the merge reads, with the sessions'
// methods tables so a file write is named by the Java method that did
// it, and writes report.json beside model.json; `iormeshctl show
// report` prints the run summary, then the rollup as text — the
// document the LLM eval (internal/eval) hands the model.

// reportFileName is the rollup in a build's output directory.
const reportFileName = "report.json"

// errMethodsCap marks a methods table left out because the method rows
// read — by build's rollup, or by query -frames (decision #42) — would
// exceed maxMethodIDs.
var errMethodsCap = fmt.Errorf("methods table left out: the method rows read exceed %d", maxMethodIDs)

// loadMethodTables reads every methods table of the loaded sessions,
// each checked in the memory-capped child first and its footer held to
// its name and place, keyed by federate session. A table that fails —
// or would take the rows past maxMethodIDs, so a hostile server cannot
// grow the build's memory through it — joins segs.Bad whole: its
// session's I/O then names no method, and the build (or query -frames,
// whose ids then print bare) exits 1 saying so.
func loadMethodTables(ctx context.Context, segs *federate.Segments) (map[string]map[uint64]schema.Method, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	check := childChecker(exe, checkAddressSpace)
	sessions := map[string]bool{}
	for _, s := range segs.Sources {
		sessions[s.Session()] = true
	}
	out := map[string]map[uint64]schema.Method{}
	n := 0
	for _, t := range segs.TableFiles {
		// A table an earlier read already left out (query's -method,
		// before -frames) is named once, not twice.
		if t.File.Table != segment.TableMethods || !sessions[t.Session()] || isBad(segs.Bad, t.Name()) {
			continue
		}
		rows := map[uint64]schema.Method{}
		err := readMethodTable(ctx, check, t, func(m *schema.Method) error {
			if n+len(rows) >= maxMethodIDs {
				return errMethodsCap
			}
			rows[m.MethodID] = *m
			return nil
		})
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			segs.Bad = append(segs.Bad, federate.BadSegment{Name: t.Name(), Err: err})
			continue
		}
		mergeRows(out, t.Session(), rows)
		n += len(rows)
	}
	return out, nil
}

// mergeRows adds a table's rows to its session's: a session's segments
// each carry the frames first named in them, so the union is the table.
func mergeRows(out map[string]map[uint64]schema.Method, session string, rows map[uint64]schema.Method) {
	if out[session] == nil {
		out[session] = map[uint64]schema.Method{}
	}
	for id, m := range rows {
		out[session][id] = m
	}
}

// readMethodTable checks one methods table in the child, then streams
// its rows to fn. An error names the table, never its local path; one
// fn returns (a cap) comes back as it is.
func readMethodTable(ctx context.Context, check federate.Checker, t federate.TableFile,
	fn func(*schema.Method) error) error {
	sum, err := check(ctx, t.Path)
	if err == nil {
		err = t.WantMeta(sum.Meta)
	}
	if err == nil {
		_, err = segment.ReadMethods(ctx, t.Path, t.WantMeta, fn)
	}
	if err == nil || errors.Is(err, errTooManyMethods) || errors.Is(err, errMethodsCap) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), t.Path, t.Name()))
}

// reportSegment is a segment whose events also feed the run's tally and
// rollup as the merge reads them: neither costs a pass of its own.
type reportSegment struct {
	*federate.Segment
	tally *viewmodel.Tally
	rep   *report.Collector
}

func (s reportSegment) Events(ctx context.Context, fn func(*schema.Event) error) error {
	k := viewmodel.SessionKey{Project: s.Project, Host: s.Host, FQDN: s.File.FQDN, ID: s.File.Session}
	src := report.Source{Project: s.Project, Host: s.Host, FQDN: s.File.FQDN, Session: s.Session()}
	name := s.Name()
	s.tally.Segment(k, name)
	return s.Segment.Events(ctx, func(ev *schema.Event) error {
		s.tally.Add(k, name, ev)
		s.rep.Add(src, ev)
		return fn(ev)
	})
}

// writeReport writes report.json into the build directory, atomically.
func writeReport(dir string, r report.Report) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("build: report: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(dir, reportFileName), append(data, '\n')); err != nil {
		return fmt.Errorf("build: %w", err)
	}
	return nil
}

// loadReport reads the report.json of the build of f's projects.
func loadReport(e env, f buildFlags) (report.Report, error) {
	p, err := resolveShowPaths(e, f)
	if err != nil {
		return report.Report{}, err
	}
	projects, err := buildProjects(p.store, f.projects)
	if err != nil {
		return report.Report{}, err
	}
	path := filepath.Join(p.builds, f.key(projects), reportFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return report.Report{}, fmt.Errorf("show: no report for the build of %s (iormeshctl build)",
			f.name(projects))
	}
	if err != nil {
		return report.Report{}, fmt.Errorf("show: %w", err)
	}
	var r report.Report
	if err := json.Unmarshal(data, &r); err != nil {
		return report.Report{}, fmt.Errorf("show: %s: %w", path, err)
	}
	return r, nil
}

// showReport prints the report view: the run summary, then the rollup
// (or its JSON with -json, terminal-safe: printJSON).
func showReport(e env, f buildFlags, m viewmodel.Model, asJSON bool) error {
	r, err := loadReport(e, f)
	if err != nil {
		return err
	}
	if asJSON {
		return printJSON(e.stdout, r)
	}
	if err := viewmodel.WriteSummary(e.stdout, m); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(e.stdout); err != nil {
		return err
	}
	return report.WriteText(e.stdout, r)
}

// isBad reports whether name is already among the left-out segments.
func isBad(bad []federate.BadSegment, name string) bool {
	for _, b := range bad {
		if b.Name == name {
			return true
		}
	}
	return false
}

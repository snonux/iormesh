package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"iormesh/internal/hostlayout"
	"iormesh/internal/project"
	"iormesh/internal/release"
	"iormesh/internal/segment"
)

// installRoot is what uninstall removes: the binary and the default
// trace dir under it (PLAN.md M4 "Cleanup"). It is deliberately not a
// flag: uninstall runs under the one sudoers rule, and a path argument
// would turn it into "delete any tree as root". The path comes from
// internal/hostlayout, which the controller's bootstrap reads too.
const installRoot = hostlayout.Root

// statusReport is one project's line in `iormesh status` (-json prints
// the reports as they are).
type statusReport struct {
	Project string `json:"project"`
	Unit    string `json:"unit"`
	// UnitState is systemd's view, e.g. "active/running" or "not loaded".
	UnitState string `json:"unit_state"`
	// State is state.json's, with "dead" for a running state whose
	// process is gone; empty without a state file.
	State   string        `json:"state,omitempty"`
	Session *sessionState `json:"session,omitempty"`
	// ReadySegments counts the published events segments in ready/ that
	// await collection.
	ReadySegments int    `json:"ready_segments"`
	Error         string `json:"error,omitempty"`
}

// projectFlags are the -project and -trace-dir flags stop and status
// share: the unit's name and where its state.json is.
type projectFlags struct {
	project, traceDir *string
}

func addProjectFlags(fs *flag.FlagSet) projectFlags {
	return projectFlags{
		project:  fs.String("project", "", "project label"),
		traceDir: fs.String("trace-dir", defaultTraceDir, "the trace dir the session writes to"),
	}
}

// parseFlags parses args into fs, which takes no positional arguments.
func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: unexpected argument %q", errUsage, fs.Arg(0))
	}
	return nil
}

// stop stops the project's unit: systemd sends SIGTERM, the run drains,
// writes its end event and publishes the active segment as its last
// (decision #19). It then prints the end state.json recorded, and fails
// when there is none: the stop happened, the clean end did not.
func (c control) stop(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	pf := addProjectFlags(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	unit, err := unitName(*pf.project)
	if err != nil {
		return err
	}
	u, err := showUnit(ctx, c.sys, unit)
	if err != nil {
		return err
	}
	if !u.live() {
		return fmt.Errorf("project %s: %s is not running (%s)", *pf.project, unit, u)
	}
	if err := stopUnit(ctx, c.sys, unit); err != nil {
		return err
	}
	// The unit is gone either way, but only an `ended` state says the run
	// drained and published its last segment: a run SIGKILLed after
	// TimeoutStopSec (or one that never attached) leaves its active minute
	// unpublished. The caller — iormeshctl rm, before an uninstall that
	// would delete that file — must learn of it, so this is an error.
	st, err := readState(filepath.Join(*pf.traceDir, *pf.project))
	if err != nil {
		return fmt.Errorf("stopped %s, but its end is unknown, the last segment may be unpublished: %w", unit, err)
	}
	if st.State != stateEnded {
		return fmt.Errorf("stopped %s, but state.json says %q, not ended: the run did not finish "+
			"(killed after the stop timeout?), its last segment may be unpublished", unit, st.State)
	}
	_, _ = fmt.Fprintf(c.stdout, "iormesh: stopped %s: session %s ended (%s) at %s\n",
		unit, st.Session, st.EndReason, st.EndedAt.Format(time.RFC3339))
	return nil
}

// status reports the project's session, or with no -project every session
// it knows of: the projects with a unit loaded and those with a state.json
// in the trace dir. A named project with neither, or a corrupt state, is
// an error (after every report is printed).
func (c control) status(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	pf := addProjectFlags(fs)
	asJSON := fs.Bool("json", false, "print the reports as JSON")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	projects, err := c.statusProjects(ctx, *pf.project, *pf.traceDir)
	if err != nil {
		return err
	}
	reports := make([]statusReport, 0, len(projects))
	var errs []error
	for _, p := range projects {
		r := c.statusOf(ctx, *pf.traceDir, p)
		if r.Error != "" {
			errs = append(errs, fmt.Errorf("project %s: %s", p, r.Error))
		}
		reports = append(reports, r)
	}
	if err := c.printStatus(reports, *asJSON); err != nil {
		return err
	}
	return errors.Join(errs...)
}

// statusProjects is the project list status reports on.
func (c control) statusProjects(ctx context.Context, label, traceDir string) ([]string, error) {
	if label != "" {
		if _, err := unitName(label); err != nil {
			return nil, err
		}
		return []string{label}, nil
	}
	projects, err := listUnits(ctx, c.sys)
	if err != nil {
		return nil, err
	}
	states, _ := filepath.Glob(filepath.Join(traceDir, "*", StateFile))
	for _, s := range states {
		// A directory that is no project label is not ours to report on,
		// and its name must not reach a systemctl argv.
		if p := filepath.Base(filepath.Dir(s)); project.Valid(p) {
			projects = append(projects, p)
		}
	}
	slices.Sort(projects)
	return slices.Compact(projects), nil
}

// statusOf builds one project's report.
func (c control) statusOf(ctx context.Context, traceDir, label string) statusReport {
	r := statusReport{Project: label, Unit: unitPrefix + label + unitSuffix}
	u, err := showUnit(ctx, c.sys, r.Unit)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	r.UnitState = u.String()
	dir := filepath.Join(traceDir, label)
	r.ReadySegments = countReady(filepath.Join(dir, segment.ReadyDir))
	st, err := readState(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if u.LoadState == "not-found" {
			r.Error = "no session (no unit, no " + StateFile + " in " + dir + ")"
		}
	case err != nil:
		r.Error = err.Error()
	case st.Project != label:
		r.Error = fmt.Sprintf("%s names project %q", filepath.Join(dir, StateFile), st.Project)
	default:
		r.State, r.Session = st.effective(), &st
	}
	return r
}

// countReady counts the events segments in ready/ (not their tables):
// what the controller's next collect will fetch.
func countReady(ready string) int {
	names, _ := filepath.Glob(filepath.Join(ready, "*"+segment.Ext))
	n := 0
	for _, name := range names {
		base := strings.TrimSuffix(filepath.Base(name), segment.Ext)
		if i := strings.LastIndexByte(base, '-'); i >= 0 && !strings.Contains(base[i:], ".") {
			n++
		}
	}
	return n
}

func (c control) printStatus(reports []statusReport, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(reports)
	}
	if len(reports) == 0 {
		_, _ = fmt.Fprintln(c.stdout, "iormesh: no sessions")
	}
	for _, r := range reports {
		_, _ = fmt.Fprint(c.stdout, r.text())
	}
	return nil
}

// text is the report as status prints it, one project per block.
func (r statusReport) text() string {
	var b strings.Builder
	state := r.State
	if state == "" {
		state = "unknown"
	}
	fmt.Fprintf(&b, "%s: %s (unit %s %s)\n", r.Project, state, r.Unit, r.UnitState)
	if st := r.Session; st != nil {
		fmt.Fprintf(&b, "  session %s on %s, pid %d, tracing %s\n", st.Session, st.FQDN, st.PID, st.Targets)
		fmt.Fprintf(&b, "  started %s, ends by %s\n", st.StartedAt.Format(time.RFC3339),
			st.EndsBy.Format(time.RFC3339))
		// The run's own build (task ph4), which after an upgrade may
		// differ from the installed binary answering this status.
		if st.Version != "" {
			fmt.Fprintf(&b, "  tracer version %s\n", st.Version)
		} else {
			b.WriteString("  tracer version unknown (the state predates it)\n")
		}
		if st.State == stateEnded {
			fmt.Fprintf(&b, "  ended %s (%s)\n", st.EndedAt.Format(time.RFC3339), st.EndReason)
		}
	}
	if r.State == stateDead {
		b.WriteString("  the tracer died without its end: the active minute was not published\n")
	}
	fmt.Fprintf(&b, "  ready/: %d segments to collect\n", r.ReadySegments)
	if r.Error != "" {
		fmt.Fprintf(&b, "  error: %s\n", r.Error)
	}
	return b.String()
}

// uninstall stops every iormesh unit, then removes installRoot and the
// sudoers rule iormeshctl bootstrap installed (task qh4).
func (c control) uninstall(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	return c.uninstallFrom(ctx, installRoot, hostlayout.SudoersFile)
}

// uninstallFrom is uninstall over root (a temporary directory in tests).
// Every unit is stopped first, so each running tracer publishes its last
// segment before the tree goes. A unit that will not stop stops the
// uninstall: removing the binary under a live tracer would leave it
// running with no way to reach it by name. A start is confined to the
// trace dir /opt/iormesh/traces (confine.go), which lies under
// installRoot, so its segments go with the tree; the agent spools under
// /run/iormesh/spool are outside root and left alone (/run is emptied at
// boot). Once the tree is gone the bootstrap's sudoers rule goes too
// (removeBootstrapRule; sudoers is a temporary path in tests): it names
// a binary that no longer exists, and a host bootstrapped again gets a
// new one. A failed removal of the tree keeps the rule, so the operator
// can rerun uninstall through it.
func (c control) uninstallFrom(ctx context.Context, root, sudoers string) error {
	projects, err := listUnits(ctx, c.sys)
	if err != nil {
		return err
	}
	for _, p := range projects {
		unit := unitPrefix + p + unitSuffix
		if err := stopUnit(ctx, c.sys, unit); err != nil {
			return fmt.Errorf("uninstall: %w", err)
		}
		_, _ = fmt.Fprintf(c.stdout, "iormesh: stopped %s\n", unit)
	}
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("uninstall: %w", err)
	}
	_, _ = fmt.Fprintf(c.stdout, "iormesh: removed %s\n", root)
	return c.removeBootstrapRule(sudoers)
}

// maxSudoersSize bounds what uninstall reads of the rule file: the
// bootstrap's is two short lines.
const maxSudoersSize = 4 << 10

// removeBootstrapRule removes the sudoers file at path when it is the one
// iormeshctl bootstrap installed: a regular file, not a symlink (read
// with O_NOFOLLOW by release.ReadFile), whose first line is
// hostlayout.SudoersMarker. The path is fixed, never the caller's:
// uninstall runs through the rule. Anything else there — a rule
// configuration management laid down, a planted symlink — is left alone
// and reported, so uninstall only ever removes the privilege bootstrap
// granted. No file is nothing to do.
func (c control) removeBootstrapRule(path string) error {
	data, err := release.ReadFile(path, maxSudoersSize, nil)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		_, _ = fmt.Fprintf(c.stdout, "iormesh: left %s: not the bootstrap's rule: %v\n", path, err)
		return nil
	}
	first, _, _ := strings.Cut(string(data), "\n")
	if first != hostlayout.SudoersMarker {
		_, _ = fmt.Fprintf(c.stdout, "iormesh: left %s: not installed by iormeshctl bootstrap\n", path)
		return nil
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("uninstall: sudoers rule: %w", err)
	}
	_, _ = fmt.Fprintf(c.stdout, "iormesh: removed %s\n", path)
	return nil
}

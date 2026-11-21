package cli

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"iormesh/internal/project"
)

// The tracer's systemd units (PLAN.md M4, decision #19): one transient
// service per project, iormesh-<project>.service. The suffix is always
// spelled out: a project label may contain dots, and systemd-run would
// read a label like "x.socket" as a different unit type.
const (
	unitPrefix = "iormesh-"
	unitSuffix = ".service"
)

// commander runs a systemd tool (systemd-run, systemctl) with an argv —
// never a shell string — and returns its stdout. It is the seam the unit
// tests replace with a fake, so they never start, stop or even list a unit
// on the host that runs them.
type commander interface {
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

// execCommander runs the real tools. Their stderr is folded into the
// error, since systemctl says there why a unit could not start or stop —
// bounded and terminal-safe (boundedLines), as a tool on a compromised
// host may print anything.
type execCommander struct{}

// unitState is the part of `systemctl show` that start, stop and status
// read.
type unitState struct {
	LoadState, ActiveState, SubState, MainPID, InvocationID string
}

var _ commander = execCommander{}

func (execCommander) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(boundedLines(stderr.Bytes()))
		if msg == "" {
			msg = "(no stderr)"
		}
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, msg)
	}
	return out, nil
}

// unitName is project's unit. The label rule (internal/project) keeps it
// a valid unit name with no escaping; an invalid label is a usage error
// here, before it reaches any systemd argv.
func unitName(label string) (string, error) {
	if !project.Valid(label) {
		return "", fmt.Errorf("%w: -project %q: want %s", errUsage, label, project.Rule)
	}
	return unitPrefix + label + unitSuffix, nil
}

// showUnit reads unit's state. A unit systemd does not know is not an
// error: LoadState is then "not-found" and ActiveState "inactive", which
// is how a stopped (and, with --collect, garbage-collected) session looks.
func showUnit(ctx context.Context, sys commander, unit string) (unitState, error) {
	out, err := sys.Output(ctx, "systemctl", "show",
		"--property=LoadState,ActiveState,SubState,MainPID,InvocationID", "--", unit)
	if err != nil {
		return unitState{}, err
	}
	var st unitState
	for line := range strings.Lines(string(out)) {
		key, val, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch key {
		case "LoadState":
			st.LoadState = val
		case "ActiveState":
			st.ActiveState = val
		case "SubState":
			st.SubState = val
		case "MainPID":
			st.MainPID = val
		case "InvocationID":
			st.InvocationID = val
		}
	}
	if st.ActiveState == "" {
		return unitState{}, fmt.Errorf("systemctl show %s: no ActiveState in %q", unit, out)
	}
	return st, nil
}

// live reports whether the unit runs or is on its way up or down: a
// start then must not launch a second tracer for the project.
func (u unitState) live() bool {
	switch u.ActiveState {
	case "active", "activating", "deactivating", "reloading", "refreshing":
		return true
	}
	return false
}

// String is the unit's state as status prints it, e.g. active/running.
func (u unitState) String() string {
	if u.LoadState == "not-found" {
		return "not loaded"
	}
	return u.ActiveState + "/" + u.SubState
}

// listUnits returns the projects that have a unit loaded (running or
// not), from `systemctl list-units`. Only names of our exact shape count:
// iormesh-<valid project>.service.
func listUnits(ctx context.Context, sys commander) ([]string, error) {
	out, err := sys.Output(ctx, "systemctl", "list-units", "--all", "--plain", "--no-legend",
		"--full", "--type=service", "--", unitPrefix+"*"+unitSuffix)
	if err != nil {
		return nil, err
	}
	var projects []string
	for line := range strings.Lines(string(out)) {
		// The first field ending in .service is the name: a failed unit's
		// line may start with a status mark.
		for _, f := range strings.Fields(line) {
			if !strings.HasSuffix(f, unitSuffix) {
				continue
			}
			label, ok := strings.CutPrefix(strings.TrimSuffix(f, unitSuffix), unitPrefix)
			if ok && project.Valid(label) {
				projects = append(projects, label)
			}
			break
		}
	}
	return projects, nil
}

// stopUnit stops unit and waits for it: systemd sends the KillSignal
// (SIGTERM), the tracer publishes its active segment and writes its end;
// TimeoutStopSec later systemd would SIGKILL it. A failed unit left
// loaded is reset, so the next start can reuse the name (with --collect
// that is rare: only a unit started otherwise stays failed).
func stopUnit(ctx context.Context, sys commander, unit string) error {
	if _, err := sys.Output(ctx, "systemctl", "stop", "--", unit); err != nil {
		return err
	}
	st, err := showUnit(ctx, sys, unit)
	if err == nil && st.ActiveState == "failed" {
		_, err = sys.Output(ctx, "systemctl", "reset-failed", "--", unit)
	}
	return err
}

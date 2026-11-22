package ctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"iormesh/internal/federate"
	"iormesh/internal/k8s"
)

// `iormeshctl status [-project NAME]... [-registry FILE] [K8S]
// [HOST|POD...]` (PLAN.md M4 "No management plane", task ph4): one table
// row per target and project with the tracer's state, end reason, ends-by,
// tracer version and the segments in ready/ awaiting collection. It
// composes what the tracers already report and changes nothing:
//
//   - a host answers `sudo -n /opt/iormesh/bin/iormesh status -json`
//     through the sudoers rule, which already lets status through
//     (internal/cli confine.go) — one call per host, every session it
//     knows of, a registered project it does not know being "none" and a
//     session the registry does not list shown too (a dropped project
//     still running is worth seeing);
//   - a pod answers `iormesh ready -project P -status` per project in its
//     newest tracer container through the kubectl transport, since the
//     container has no systemd for `iormesh status`; an image from before
//     -status answers the attach state `ready -state` instead, its
//     version then unknown.
//
// A target that fails is a row saying why, never an abort: the others are
// still asked, and the command then exits 1. Everything a server or a
// cluster answers reaches the terminal through safe or errText.

// remoteStatus is the part of the tracer's status report (internal/cli
// statusReport, sessionState) the table shows. internal/ctl cannot import
// internal/cli (cgo), so it decodes its own copy; a field an older tracer
// does not write decodes empty and shows as unknown.
type remoteStatus struct {
	Project       string         `json:"project"`
	UnitState     string         `json:"unit_state"`
	State         string         `json:"state"`
	Session       *remoteSession `json:"session"`
	ReadySegments int            `json:"ready_segments"`
	Error         string         `json:"error"`
}

// remoteSession is the part of state.json the table shows.
type remoteSession struct {
	EndsBy    time.Time `json:"ends_by"`
	EndReason string    `json:"end_reason"`
	Version   string    `json:"version"`
}

// statusRow is one line of the table. failed marks a row whose target or
// project could not be asked; note says why, or what else to know.
type statusRow struct {
	target, project string
	st              remoteStatus
	note            string
	failed          bool
}

// statusCommand parses status's flags, resolves the targets as collect
// does (every registered one by default; an unregistered one fails before
// any is contacted) and prints the table.
func statusCommand(args []string, e env) error {
	fs := newFlagSet("status", e.stderr)
	var projects projectList
	fs.Var(&projects, "project", "report only this project (repeatable; default: every project)")
	regPath := fs.String("registry", "", "registry file (default ~/.config/iormesh/servers.yaml)")
	kf := addK8sFlags(fs, false)
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	r, err := loadRegistry(*regPath, e)
	if err != nil {
		return err
	}
	targets, err := collectTargets(r, fs.Args(), projects, kf)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		_, err := fmt.Fprintln(e.stdout, "no hosts or pods registered")
		return err
	}
	var rows []statusRow
	failed := 0
	for _, tg := range targets {
		tr := targetStatus(e, tg, len(projects) == 0)
		if slices.ContainsFunc(tr, func(r statusRow) bool { return r.failed }) {
			failed++
		}
		rows = append(rows, tr...)
	}
	if err := printStatusTable(e.stdout, rows); err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("status failed on %d of %d targets", failed, len(targets))
	}
	return nil
}

// targetStatus asks one target under its own deadline (env.onHost). A
// failure of the whole target (unreachable, the API server refusing) is
// one failed row per project it carries.
func targetStatus(e env, tg collectTarget, unlisted bool) []statusRow {
	var rows []statusRow
	err := e.onHost(e.root(), func(ctx context.Context) error {
		var err error
		if tg.pod != nil {
			rows, err = podEntryStatus(ctx, *tg.pod, tg.projects)
		} else {
			rows, err = hostStatus(ctx, e, tg, unlisted)
		}
		return err
	})
	if err != nil {
		rows = nil
		for _, p := range tg.projects {
			rows = append(rows, statusRow{target: tg.host, project: p, note: errText(err), failed: true})
		}
	}
	return rows
}

// hostStatus runs the installed tracer's `status -json` on the host and
// turns its reports into rows: one per registered project, in the
// registry's order, then (when unlisted) every session the host reports
// that the registry does not list. status exits 1 when a project's
// report carries an error but still prints every report, so the output
// is read whatever the exit; only output that is no report list fails.
func hostStatus(ctx context.Context, e env, tg collectTarget, unlisted bool) ([]statusRow, error) {
	t, err := e.transport(tg.host)
	if err != nil {
		return nil, err
	}
	out, runErr := t.Run(ctx, sudoTracer("status", "-json")...)
	var reports []remoteStatus
	if err := json.Unmarshal(out, &reports); err != nil {
		if runErr != nil {
			return nil, runErr
		}
		return nil, fmt.Errorf("status -json: unreadable answer: %w", err)
	}
	byProject := map[string]remoteStatus{}
	for _, r := range reports {
		byProject[r.Project] = r
	}
	var rows []statusRow
	for _, p := range tg.projects {
		r, ok := byProject[p]
		delete(byProject, p)
		if !ok {
			rows = append(rows, statusRow{target: tg.host, project: p, st: remoteStatus{State: "none"},
				note: "no session: never started here"})
			continue
		}
		rows = append(rows, reportRow(tg.host, r, ""))
	}
	for _, r := range reports {
		if _, extra := byProject[r.Project]; extra && unlisted {
			rows = append(rows, reportRow(tg.host, r, "not in the registry"))
		}
	}
	return rows, nil
}

// reportRow is the row of one tracer report; a report carrying an error
// (a corrupt state.json, systemctl failing) is a failed row.
func reportRow(target string, r remoteStatus, note string) statusRow {
	row := statusRow{target: target, project: r.Project, st: r, note: note}
	switch {
	case r.Error != "":
		row.note, row.failed = r.Error, true
	case r.State == "" && r.UnitState != "":
		row.note = "unit " + r.UnitState + ", no state yet"
	case r.State == k8s.AttachDead && note == "":
		row.note = "the tracer died without its end: its active minute was not published"
	}
	return row
}

// podEntryStatus asks every pod the entry is now. No running pod is a
// failure, as for collect: there is no tracer to ask.
func podEntryStatus(ctx context.Context, pe podEntry, projects []string) ([]statusRow, error) {
	pods, err := pe.pods(ctx)
	if err != nil {
		return nil, err
	}
	if len(pods) == 0 {
		return nil, errors.New("no running pod")
	}
	var rows []statusRow
	for _, st := range pods {
		rows = append(rows, podStatus(ctx, pe, st, projects)...)
	}
	return rows, nil
}

// podStatus is one pod's rows. A pod without a tracer container, or whose
// tracer container stopped (kubectl exec cannot enter it), says so in its
// rows without an exec; neither is a failure of status.
func podStatus(ctx context.Context, pe podEntry, st k8s.PodState, projects []string) []statusRow {
	label := pe.label(st.Name)
	row := func(p string) statusRow { return statusRow{target: label, project: p} }
	var rows []statusRow
	if !st.Tracer || st.Stopped != "" {
		for _, p := range projects {
			r := row(p)
			r.st.State, r.note = "stopped", "tracer container "+st.Container+" "+st.Stopped
			if !st.Tracer {
				r.st.State, r.note = "none", "no "+k8s.TracerContainer+" tracer container"
			}
			rows = append(rows, r)
		}
		return rows
	}
	t, err := pe.transport(st.Name, st.Container)
	for _, p := range projects {
		if err != nil {
			r := row(p)
			r.note, r.failed = errText(err), true
			rows = append(rows, r)
			continue
		}
		rows = append(rows, podProjectStatus(ctx, t, label, p))
	}
	return rows
}

// podProjectStatus asks one pod's tracer for one project's report. An
// image from before -status refuses the flag (a remote exit status); it
// is then asked `ready -state`, whose answer has the state and an end
// reason but no version, ends-by or ready/ count.
func podProjectStatus(ctx context.Context, t federate.Transport, label, p string) statusRow {
	row := statusRow{target: label, project: p}
	out, err := t.Run(ctx, k8s.TracerBinary, "ready", "-project", p, k8s.ReadyStatusFlag)
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		if jerr := json.Unmarshal(out, &row.st); jerr != nil {
			row.note, row.failed = "ready -status: unreadable answer: "+errText(jerr), true
		}
		row.st.Project = p
		if row.st.State == "" && !row.failed {
			row.st.State = k8s.AttachPending // no state.json yet: not attached
		}
		return row
	case !errors.As(err, &exitErr):
		row.note, row.failed = errText(err), true
		return row
	}
	out, err = t.Run(ctx, k8s.TracerBinary, "ready", "-project", p, k8s.ReadyStateFlag)
	if err != nil {
		row.note, row.failed = errText(err), true
		return row
	}
	words := strings.Fields(string(out))
	row.st.ReadySegments = -1
	row.note = "an image from before " + k8s.ReadyStatusFlag
	if len(words) == 0 || words[0] == k8s.AttachPending {
		row.st.State = k8s.AttachPending
		return row
	}
	row.st.State, row.st.Session = words[0], &remoteSession{}
	if len(words) > 1 && row.st.State == k8s.AttachEnded && !strings.HasPrefix(words[1], k8s.AttachStartedKey) {
		row.st.Session.EndReason = words[1]
	}
	return row
}

// printStatusTable prints rows as an aligned table. Every value passes
// through safe: states, reasons, versions and notes are the servers'.
func printStatusTable(w io.Writer, rows []statusRow) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TARGET\tPROJECT\tSTATE\tEND-REASON\tENDS-BY\tVERSION\tUNCOLLECTED\tNOTE")
	for _, r := range rows {
		state, reason, endsBy, ver, ready := r.cells()
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", safe(r.target), safe(r.project),
			safe(state), safe(reason), safe(endsBy), safe(ver), safe(ready), safe(r.note))
	}
	return tw.Flush()
}

// cells are a row's state, end reason, ends-by, version and ready/ count
// as the table shows them: "-" where the column does not apply, "unknown"
// where the tracer did not say (a state.json from before the version
// field, an image from before -status).
func (r statusRow) cells() (state, reason, endsBy, ver, ready string) {
	state, reason, endsBy, ver, ready = r.st.State, "-", "-", "-", "-"
	if r.failed {
		return "error", reason, endsBy, ver, ready
	}
	if state == "" {
		state = "unknown"
	}
	if s := r.st.Session; s != nil {
		ver = "unknown"
		if s.Version != "" {
			ver = s.Version
		}
		if state == k8s.AttachEnded && s.EndReason != "" {
			reason = s.EndReason
		}
		if state == k8s.AttachRunning && !s.EndsBy.IsZero() {
			endsBy = s.EndsBy.UTC().Format(time.RFC3339)
		}
	}
	if r.st.ReadySegments < 0 {
		ready = "unknown"
	} else if state != "none" {
		ready = strconv.Itoa(r.st.ReadySegments)
	}
	return state, reason, endsBy, ver, ready
}

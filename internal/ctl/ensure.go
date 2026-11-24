package ctl

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"iormesh/internal/federate"
	"iormesh/internal/k8s"
	"iormesh/internal/schema"
)

// `iormeshctl ensure [-project P]... [HOST|POD...]` (docs/milestones.md M4 "No
// management plane", task zh4): keep tracing over days. A session ends
// after its -duration (4h by default, 7 days at most), and its transient
// unit has no Restart=, so a reboot, an OOM kill or a disk-limit trip
// ends it for good. Per host, ensure
//
//  1. reads the installed tracer's `status -json` (hostReports, as
//     status does);
//  2. starts again, with deploy's start argv and deploy's start path
//     (deployer.start), every registered project whose session ended
//     on its own — duration, targets_exited, error, a stop that was not
//     the operator's (a reboot's SIGTERM), or a run that died;
//  3. collects every selected project of the host (collector.host);
//  4. restarts a disk_limit end only now, and only when the collect
//     emptied its ready/ (a second status read): restarting it before
//     would end it again at once, tick after tick. If the partition is
//     still over the limit for another reason, the new run's watchdog
//     refuses at its start, before it writes anything.
//
// It leaves alone a session the operator stopped (`iormesh stop` marks
// state.json, operator_stop), a live unit, and a project the host never
// ran: starting a new project is deploy's, which also upgrades; ensure
// never pushes or upgrades. A tracer from before operator_stop cannot
// tell an operator stop from a reboot's, so its "stop" ends are left
// stopped too; a dead run of such a tracer is restarted. Run again, it
// finds the sessions it started running and only collects, so it is
// safe from a timer (docs/architecture.md "Tracing for days"). A pod
// entry (task pk4) is decided by its pods' tracer containers instead
// and restarted through deploy's pod path (ensure_pods.go).

// ensureAction is what ensure does with one project's session.
type ensureAction int

const (
	ensureLeave        ensureAction = iota // nothing: running, stopped, or not ensure's to start
	ensureRestart                          // start it again now
	ensureAfterCollect                     // a disk_limit end: collect, then maybe start
	ensureFail                             // the tracer's report for it carries an error
	ensureAsk                              // a pod's running debug tracer: ask its session (ensure_pods.go)
)

// ensurer is one ensure run: the registry it resolved the targets in
// and the collector that syncs them.
type ensurer struct {
	e env
	r federate.Registry
	c collector
}

// ensureCommand parses ensure's flags, resolves the targets as collect
// does (every registered host and pod entry carrying the projects by
// default; a project none carries is refused) before contacting any,
// and ensures target by target.
func ensureCommand(args []string, e env) error {
	fs := newFlagSet("ensure", e.stderr)
	var projects projectList
	fs.Var(&projects, "project", "ensure only this project (repeatable; default: every project)")
	regPath := fs.String("registry", "", "registry file (default ~/.config/iormesh/servers.yaml)")
	store := fs.String("store", "", "local trace store (default ~/.local/share/iormesh/traces)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	r, err := loadRegistry(*regPath, e)
	if err != nil {
		return err
	}
	// ensure takes no K8S flags: a pod entry is reached as the registry
	// says, as a collect of every target reaches it.
	targets, err := collectTargets(r, fs.Args(), projects, &k8sFlags{})
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		_, err := fmt.Fprintln(e.stdout, "no hosts or pods registered")
		return err
	}
	if err := checkEnsurePods(targets); err != nil {
		return err
	}
	c, err := newCollector(e, *store)
	if err != nil {
		return err
	}
	return ensurer{e: e, r: r, c: c}.all(e.root(), targets)
}

// all ensures every target in turn — a host (host) or a pod entry
// (podEntry, ensure_pods.go) — each under its own deadline
// (env.onHost), so a target that stalls is reported and the next still
// ensured; the command then fails naming the failed targets.
func (en ensurer) all(ctx context.Context, targets []collectTarget) error {
	var failed []string
	for _, tg := range targets {
		err := en.e.onHost(ctx, func(ctx context.Context) error {
			if tg.pod != nil {
				return en.podEntry(ctx, tg)
			}
			return en.host(ctx, tg)
		})
		if err != nil {
			_, _ = fmt.Fprintf(en.e.stderr, "iormeshctl: %s: %s\n", safe(tg.host), errText(err))
			failed = append(failed, tg.host)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("ensure failed on %d of %d targets: %s", len(failed), len(targets),
			strings.Join(failed, " "))
	}
	return nil
}

// host runs ensure's steps on one host. A status that cannot be read
// fails the host before anything is started or collected: ensure acts
// only on what the tracer said. Projects are independent: a failed start
// or a report error is that project's, and the host is still collected.
func (en ensurer) host(ctx context.Context, tg collectTarget) error {
	t, err := en.e.transport(tg.host)
	if err != nil {
		return err
	}
	reports, err := hostReports(ctx, t)
	if err != nil {
		return err
	}
	errs, full, err := en.projects(ctx, t, tg, reports)
	if err != nil {
		return err
	}
	if err := en.c.host(ctx, tg); err != nil {
		for _, p := range full {
			en.say(tg.host, p.Name, "collect failed, not restarted")
		}
		return errors.Join(append(errs, err)...)
	}
	return errors.Join(append(errs, en.afterCollect(ctx, t, tg.host, full))...)
}

// projects acts on each of tg's projects by its report: restarts the
// ones that ended on their own and returns the disk_limit ends, which
// wait for the collect, with the projects' failures.
func (en ensurer) projects(ctx context.Context, t federate.Transport, tg collectTarget,
	reports []remoteStatus) (errs []error, full []federate.Project, err error) {
	srv, err := en.r.Lookup(tg.host)
	if err != nil {
		return nil, nil, err
	}
	for _, p := range srv.Projects {
		if !slices.Contains(tg.projects, p.Name) {
			continue
		}
		act, note := ensureDecide(reportOf(reports, p.Name))
		switch act {
		case ensureRestart:
			if err := en.start(ctx, t, tg.host, p); err != nil {
				note += ", restart failed"
				errs = append(errs, fmt.Errorf("project %s: %w", p.Name, err))
			} else {
				note += ", restarted"
			}
		case ensureAfterCollect:
			full = append(full, p)
		case ensureFail:
			errs = append(errs, fmt.Errorf("project %s: %s", p.Name, note))
		}
		en.say(tg.host, p.Name, note)
	}
	return errs, full, nil
}

// afterCollect restarts the disk_limit ends in full whose ready/ the
// collect emptied, from a second status read.
func (en ensurer) afterCollect(ctx context.Context, t federate.Transport, host string,
	full []federate.Project) error {
	if len(full) == 0 {
		return nil
	}
	reports, err := hostReports(ctx, t)
	if err != nil {
		return fmt.Errorf("status after collect, disk_limit ends not restarted: %w", err)
	}
	var errs []error
	for _, p := range full {
		r := reportOf(reports, p.Name)
		switch {
		case r == nil || r.Error != "":
			en.say(host, p.Name, "no readable status after the collect, not restarted")
			continue
		case r.ReadySegments > 0:
			en.say(host, p.Name, fmt.Sprintf("ended (disk_limit): %d segments still in ready/ after the "+
				"collect, not restarted", r.ReadySegments))
			continue
		}
		if err := en.start(ctx, t, host, p); err != nil {
			en.say(host, p.Name, "ended (disk_limit): restart after collecting failed")
			errs = append(errs, fmt.Errorf("project %s: %w", p.Name, err))
			continue
		}
		en.say(host, p.Name, "ended (disk_limit), restarted after collecting")
	}
	return errors.Join(errs...)
}

// start starts p on host through deploy's start path, with deploy's
// argv for the registry entry; the unit is not live (ensureDecide), so
// nothing is stopped first. The deploy record is left as it is: it holds
// what deploy started and on which build, which ensure does not know.
func (en ensurer) start(ctx context.Context, t federate.Transport, host string, p federate.Project) error {
	return deployer{e: en.e}.start(ctx, t, host, startArgs(p), false)
}

// say prints one project's outcome.
func (en ensurer) say(host, project, note string) {
	_, _ = fmt.Fprintf(en.e.stdout, "%s: project %s: %s\n", safe(host), safe(project), safe(note))
}

// reportOf is project's report among reports, nil when the host has
// none: no unit and no state.json, never started there.
func reportOf(reports []remoteStatus, project string) *remoteStatus {
	i := slices.IndexFunc(reports, func(r remoteStatus) bool { return r.Project == project })
	if i < 0 {
		return nil
	}
	return &reports[i]
}

// ensureDecide is what ensure does with a project's report, and the note
// it prints for it. Only an unlive unit is ever restarted: start refuses
// a live one, and a live one is tracing (or attaching).
func ensureDecide(r *remoteStatus) (ensureAction, string) {
	switch {
	case r == nil:
		return ensureLeave, "no session here: deploy starts it, ensure only restarts"
	case r.Error != "":
		return ensureFail, r.Error
	case liveState(strings.SplitN(r.UnitState, "/", 2)[0]):
		return ensureLeave, "running, nothing to do"
	}
	operator := r.Session != nil && r.Session.OperatorStop != nil && *r.Session.OperatorStop
	switch r.State {
	case "":
		return ensureRestart, "unit " + r.UnitState + " without a session"
	case k8s.AttachRunning:
		return ensureLeave, "running outside its unit, left alone"
	case k8s.AttachDead:
		if operator {
			return ensureLeave, "stopped by the operator, left stopped"
		}
		return ensureRestart, "dead (the tracer died without its end)"
	case k8s.AttachEnded:
		return endedDecide(r.Session, operator)
	}
	return ensureLeave, "state " + r.State + " unknown to this iormeshctl, left alone"
}

// endedDecide is ensureDecide for an ended session.
func endedDecide(s *remoteSession, operator bool) (ensureAction, string) {
	if s == nil {
		s = &remoteSession{}
	}
	switch {
	case operator:
		return ensureLeave, "stopped by the operator, left stopped"
	case s.EndReason == schema.EndStop && s.OperatorStop == nil:
		return ensureLeave, "ended (stop) on a tracer that cannot tell an operator stop from a " +
			"reboot's, left stopped (deploy upgrades it)"
	case s.EndReason == schema.EndDiskLimit:
		return ensureAfterCollect, "ended (disk_limit): collecting first"
	}
	return ensureRestart, "ended (" + s.EndReason + ")"
}

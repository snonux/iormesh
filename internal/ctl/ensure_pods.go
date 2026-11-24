package ctl

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"iormesh/internal/k8s"
)

// ensure on a pod entry (PLAN.md M4 "No management plane", task pk4). A
// pod's tracer has no systemd unit, and once its container stopped no
// `kubectl exec` can read its state.json, so a pod is decided by its
// tracer container as the API server reports it (podDecide):
//
//   - a debug tracer that terminated with a non-zero exit code — killed
//     with its app container (whose PID namespace it joins), OOM-killed,
//     crashed — ended on its own: deploy's pod path (deployPod) starts
//     the next iormesh-N with deploy's argv and waits for it to attach;
//   - one that exited 0 was ended on purpose: a -linger run whose
//     session did not fail exits only once SIGTERM or SIGINT ends it
//     (a failed one exits 1 on the signal and is restarted). It is left
//     stopped;
//   - a terminating pod (a pod: entry's, in its deletion grace period:
//     Get returns it, Select does not) is left whatever its tracer: a
//     new one would die with it before attaching;
//   - a running one is asked `ready -status` (livePod): a session ended
//     after its -duration or at its disk cap lingers in a container that
//     cannot be restarted, so ensure says a pod restart is needed;
//   - a pod without a tracer container is deploy's, as a host's project
//     that never ran; a sidecar is the kubelet's to restart.
//
// The entry's pods are then read again and collected as collect does
// (collectPods), each through its newest tracer; a tracer left stopped
// on purpose cannot be entered and is skipped with a note, not failed
// on every timer tick. A pod: entry's pod that is gone, or a selector
// matching no running pod, fails the entry as collect does.

// checkEnsurePods refuses, before any target is contacted, a debug pod
// entry whose pass-through flags deploy would refuse (checkPodFlags):
// a restart runs deploy's argv.
func checkEnsurePods(targets []collectTarget) error {
	for _, tg := range targets {
		if tg.pod == nil || tg.pod.scope.Mode != k8s.ModeDebug {
			continue
		}
		for _, p := range tg.pod.projects {
			if err := checkPodFlags(p); err != nil {
				return fmt.Errorf("pod entry %s project %s: %w", tg.host, p.Name, err)
			}
		}
	}
	return nil
}

// podEntry runs ensure's steps on every pod the entry is now: restarts
// the ended tracers, then collects. Pods are independent: one whose
// restart fails is reported and the others still ensured and collected.
func (en ensurer) podEntry(ctx context.Context, tg collectTarget) error {
	pe := *tg.pod
	pods, err := pe.pods(ctx)
	if err != nil {
		return fmt.Errorf("%w (nothing restarted or collected: deploy starts a tracer in a pod that replaces it)",
			err)
	}
	var errs []error
	for _, st := range pods {
		if err := en.pod(ctx, pe, st, tg.projects); err != nil {
			errs = append(errs, fmt.Errorf("pod %s: %w", st.Name, err))
		}
	}
	return errors.Join(append(errs, en.collectPods(ctx, pe, tg.projects))...)
}

// pod acts on one pod by podDecide and prints the outcome per project.
func (en ensurer) pod(ctx context.Context, pe podEntry, st k8s.PodState, projects []string) error {
	label := pe.label(st.Name)
	act, note := podDecide(pe, st)
	switch act {
	case ensureAsk:
		return en.livePod(ctx, pe, st, projects)
	case ensureRestart:
		if err := deployPod(ctx, en.e, pe, st.Name, k8s.DefaultAttachTimeout); err != nil {
			en.sayAll(label, projects, note+", restart failed")
			return err
		}
		note += ", restarted"
	}
	en.sayAll(label, projects, note)
	return nil
}

// podDecide is what ensure does with one pod's tracer container, and
// the note it prints; ensureAsk for a running debug tracer, whose
// session decides (livePod).
func podDecide(pe podEntry, st k8s.PodState) (ensureAction, string) {
	tracer := "tracer container " + strconv.Quote(st.Container) + " " + st.Stopped
	switch {
	case st.Terminating:
		// Only a pod: entry's Get returns one (Select skips them). It
		// still reads Running, but its tracer dies with it: a new one
		// would never attach, and the next pod is deploy's.
		return ensureLeave, "pod terminating, nothing restarted (deploy starts a tracer in its replacement)"
	case pe.scope.Mode != k8s.ModeDebug:
		return ensureLeave, "sidecar: the kubelet restarts it into each session, ensure only collects"
	case !st.Tracer:
		return ensureLeave, "no tracer container: deploy starts it, ensure only restarts"
	case stoppedOnPurpose(pe, st):
		return ensureLeave, tracer + ", stopped on purpose, left stopped"
	case st.Exited:
		return ensureRestart, tracer
	case st.Stopped != "":
		return ensureLeave, tracer + ", left alone" // waiting (an image pull), no status yet
	}
	return ensureAsk, ""
}

// stoppedOnPurpose reports a debug tracer that exited 0: a signal ended
// it (see Exited in internal/k8s PodState).
func stoppedOnPurpose(pe podEntry, st k8s.PodState) bool {
	return pe.scope.Mode == k8s.ModeDebug && st.Tracer && st.Exited && st.ExitCode == 0
}

// livePod asks a running debug tracer for each project's session, as
// status does (podProjectStatus). Nothing is restarted: deploy leaves a
// running ephemeral container alone, and none can be restarted.
func (en ensurer) livePod(ctx context.Context, pe podEntry, st k8s.PodState, projects []string) error {
	label := pe.label(st.Name)
	t, err := pe.transport(st.Name, st.Container)
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range projects {
		row := podProjectStatus(ctx, t, label, p)
		if row.failed {
			errs = append(errs, fmt.Errorf("project %s: %s", p, row.note))
		}
		en.say(label, p, liveNote(row))
	}
	return errors.Join(errs...)
}

// liveNote is livePod's note for one project's status row.
func liveNote(row statusRow) string {
	switch row.st.State {
	case k8s.AttachEnded, k8s.AttachDead:
		state := row.st.State
		if s := row.st.Session; s != nil && s.EndReason != "" {
			state += " (" + s.EndReason + ")"
		}
		return state + ": a running debug tracer cannot be restarted, restart the pod for a new session"
	case k8s.AttachPending:
		return "attaching, nothing to do"
	}
	if row.failed {
		return row.note
	}
	return "running, nothing to do"
}

// collectPods collects every pod the entry is now through collect's
// per-pod step (collector.pod), read again so a restarted pod is
// entered through its new tracer. A tracer left stopped on purpose is
// skipped with a note: kubectl exec cannot enter it, and failing on it
// would fail every timer tick over the operator's own stop.
func (en ensurer) collectPods(ctx context.Context, pe podEntry, projects []string) error {
	pods, err := pe.pods(ctx)
	if err != nil {
		return fmt.Errorf("collect: %w", err)
	}
	var errs []error
	for _, st := range pods {
		if stoppedOnPurpose(pe, st) {
			en.sayAll(pe.label(st.Name), projects, "collect skipped: kubectl exec cannot enter a stopped "+
				"tracer; its published segments wait in the emptyDir for the next deploy, or go with the pod")
			continue
		}
		if err := en.c.pod(ctx, pe, st, projects); err != nil {
			errs = append(errs, fmt.Errorf("pod %s: %w", st.Name, err))
		}
	}
	return errors.Join(errs...)
}

// sayAll prints note for each of projects on target.
func (en ensurer) sayAll(target string, projects []string, note string) {
	for _, p := range projects {
		en.say(target, p, note)
	}
}

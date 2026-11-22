package ctl

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"iormesh/internal/federate"
	"iormesh/internal/k8s"
)

// splitTargets separates pod entry references from host names.
func splitTargets(args []string) (hosts, refs []string) {
	for _, a := range args {
		if federate.IsPodRef(a) {
			refs = append(refs, a)
		} else {
			hosts = append(hosts, a)
		}
	}
	return hosts, refs
}

// podDeployTargets resolves the pod entries refs name, narrowed to
// project when set, with every remaining project's flags checked — all
// before any pod or host is contacted. A reference named twice is
// deployed once.
func podDeployTargets(r *federate.Registry, refs []string, project string, kf *k8sFlags) ([]podEntry, error) {
	var out []podEntry
	for _, ref := range refs {
		if slices.ContainsFunc(out, func(pe podEntry) bool { return pe.ref == ref }) {
			continue
		}
		pe, err := resolvePodRef(r, ref, kf)
		if err != nil {
			return nil, err
		}
		if project != "" {
			i := slices.IndexFunc(pe.projects, func(p federate.Project) bool { return p.Name == project })
			if i < 0 {
				return nil, fmt.Errorf("%w: %q on %s", federate.ErrUnknownProject, project, ref)
			}
			pe.projects = []federate.Project{pe.projects[i]}
		}
		for _, p := range pe.projects {
			if err := checkPodFlags(p); err != nil {
				return nil, fmt.Errorf("pod entry %s project %s: %w", ref, p.Name, err)
			}
		}
		out = append(out, pe)
	}
	return out, nil
}

// deployPods deploys every entry's pods in turn, a debug tracer waited
// for up to attach until attached; a failed pod is reported and the next
// one still deployed, and the result names them. The entry's pod lookup
// and each pod's deploy run under their own per-host bound (env.onHost,
// task pi4), as a server's deploy does, so a kubectl that stalls past
// its own bounds (internal/k8s callTimeout) still cannot hold the rest.
func deployPods(ctx context.Context, e env, entries []podEntry, attach time.Duration) error {
	var failed []string
	for _, pe := range entries {
		var pods []k8s.PodState
		err := e.onHost(ctx, func(ctx context.Context) error {
			var err error
			pods, err = pe.pods(ctx)
			return err
		})
		if err != nil {
			_, _ = fmt.Fprintf(e.stderr, "iormeshctl: %s: %s\n", pe.ref, errText(err))
			failed = append(failed, pe.ref)
			continue
		}
		for _, st := range pods {
			err := e.onHost(ctx, func(ctx context.Context) error { return deployPod(ctx, e, pe, st.Name, attach) })
			if err != nil {
				_, _ = fmt.Fprintf(e.stderr, "iormeshctl: %s: %s\n", pe.label(st.Name), errText(err))
				failed = append(failed, pe.label(st.Name))
			}
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("deploy failed on %d pod targets: %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}

// deployPod is k8s.Kubectl.Deploy on one pod, with the entry's project
// as the debug container's tracer argv (a debug entry has exactly one;
// a sidecar's command line is its patch's, so none is passed). A debug
// tracer, new or left running, is then waited for until it reports its
// probes attached (k8s.Kubectl.WaitAttached, task ef4): kubectl debug
// returns before the image is even pulled, and a workload started on
// deploy's word would otherwise go untraced. One that never attaches,
// or whose session already ended, fails the pod.
func deployPod(ctx context.Context, e env, pe podEntry, pod string, attach time.Duration) error {
	k, err := pe.transport(pod, "")
	if err != nil {
		return err
	}
	var argv []string
	if pe.scope.Mode == k8s.ModeDebug {
		argv = podRunArgs(pe.projects[0])
	}
	out, err := k.Deploy(ctx, argv...)
	if err != nil {
		return err
	}
	label := pe.label(pod)
	var session string
	if out == k8s.Started || out == k8s.Running || out == k8s.Replaced {
		if session, err = k.WaitAttached(ctx, pe.projects[0].Name, attach); err != nil {
			return err
		}
	}
	switch out {
	case k8s.Started:
		_, err = fmt.Fprintf(e.stdout, "%s: started tracer, project %s, image %s; attached, session %s\n", label,
			pe.projects[0].Name, pe.scope.Image, session)
	case k8s.Replaced:
		_, err = fmt.Fprintf(e.stdout, "%s: earlier tracer ended (its unsealed segment is lost, its published "+
			"ones stay for collect); started tracer %s, project %s, image %s; attached, session %s\n", label,
			k.Tracer(), pe.projects[0].Name, pe.scope.Image, session)
	case k8s.Running:
		_, err = fmt.Fprintf(e.stdout, "%s: tracer %s already running the pinned image, attached, session %s; "+
			"left alone (a running ephemeral container cannot be restarted: restart the pod for a new session)\n",
			label, k.Tracer(), session)
	case k8s.SidecarVerified:
		_, err = fmt.Fprintf(e.stdout, "%s: sidecar running the pinned image; its sessions are the "+
			"sidecar patch's command line\n", label)
	default:
		err = errors.New("unknown deploy outcome")
	}
	return err
}

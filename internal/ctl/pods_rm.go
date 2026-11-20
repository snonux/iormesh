package ctl

import (
	"context"
	"fmt"
	"slices"

	"iormesh/internal/federate"
)

// rmPodOptions are rm's flags as they apply to a pod entry.
type rmPodOptions struct {
	regPath, store, project string
	force                   bool
	kf                      *k8sFlags
}

// rmPod is `iormeshctl rm [-project P] [-force] REF` for a pods: entry
// (pods.go): collect the entry's projects from every pod it is now, then
// drop the entry (or the one project) from the registry. A collect that
// fails — no running pod, a pod kubectl cannot reach — leaves the
// registry unchanged unless -force: the pod's emptyDir may still hold
// what was not collected, and an entry dropped by mistake is only an
// edit away, but the traces would not be. Nothing in the pod is stopped
// (see pods.go), and the output says so.
func rmPod(ref string, o rmPodOptions, e env) error {
	r, err := loadRegistry(o.regPath, e)
	if err != nil {
		return err
	}
	pe, err := resolvePodRef(&r, ref, o.kf)
	if err != nil {
		return err
	}
	var projects []string
	for _, p := range pe.projects {
		projects = append(projects, p.Name)
	}
	if o.project != "" {
		if !slices.Contains(projects, o.project) {
			return fmt.Errorf("%w: %q on %s", federate.ErrUnknownProject, o.project, ref)
		}
		projects = []string{o.project}
	}
	c, err := newCollector(e, o.store)
	if err != nil {
		return err
	}
	if err := c.podEntryCollect(context.Background(), pe, projects); err != nil {
		if !o.force {
			return fmt.Errorf("%s: last collect: %w (registry unchanged; rm -force drops the entry anyway "+
				"and loses what was not collected)", ref, err)
		}
		_, _ = fmt.Fprintf(e.stderr, "iormeshctl: warning: %s: last collect: %s; -force: dropping the entry\n",
			ref, errText(err))
	}
	_, _ = fmt.Fprintf(e.stdout, "%s: the tracer in its pods is not stopped: an ephemeral container runs "+
		"until its -duration ends or the pod goes, a sidecar until its patch is reverted\n", ref)
	what := "removed " + ref
	if o.project != "" {
		what += " project " + o.project
	}
	return editRegistry(o.regPath, e, func(r *federate.Registry) error {
		return r.RemovePod(ref, o.project)
	}, what)
}

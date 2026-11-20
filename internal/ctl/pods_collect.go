package ctl

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"iormesh/internal/federate"
	"iormesh/internal/k8s"
)

// cutFlag splits a "-key=value" or "--key" argument; key is "" for an
// argument that is not a flag.
func cutFlag(f string) (key, val string, hasVal bool) {
	if !strings.HasPrefix(f, "-") {
		return "", "", false
	}
	return strings.Cut(strings.TrimLeft(f, "-"), "=")
}

// podEntryCollect collects the entry's projects from every pod it is
// now. No running pod (a pod: entry's pod gone or pending, a selector
// matching none) is a failure, never "nothing to collect": the pod's
// emptyDir went with it. Pods are independent: one that fails does not
// keep the others from being collected.
func (c collector) podEntryCollect(ctx context.Context, pe podEntry, projects []string) error {
	pods, err := pe.pods(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, st := range pods {
		if err := c.pod(ctx, pe, st, projects); err != nil {
			errs = append(errs, fmt.Errorf("pod %s: %w", st.Name, err))
		}
	}
	return errors.Join(errs...)
}

// pod collects projects from one pod into <store>/<project>/pod-<uid>,
// after pinning that directory to the pod's host identity. A pod without
// a tracer container — the API server says so — has nothing to collect;
// one whose exec fails is an error.
func (c collector) pod(ctx context.Context, pe podEntry, st k8s.PodState, projects []string) error {
	label := pe.label(st.Name)
	if !st.Tracer {
		_, err := fmt.Fprintf(c.e.stdout, "%s: no %q tracer container, nothing to collect\n", label,
			k8s.TracerContainer)
		return err
	}
	dir := k8s.PodHostID(st.UID)
	if err := c.pin(dir, pe.podIdentity(st)); err != nil {
		return err
	}
	t, err := pe.transport(st.Name)
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range projects {
		remote := path.Join(k8s.TraceRoot, p, readyDir)
		if err := c.fetch(ctx, t, label, dir, p, remote); err != nil {
			errs = append(errs, fmt.Errorf("project %s: %w", p, err))
		}
	}
	return errors.Join(errs...)
}

// pin records dir's host identity in the pins file build holds host
// directories to (hosts.json beside the service database), unless dir
// is pinned already: a pin is never overwritten (pinNewHosts). The
// identity comes from the API server, so a pod cannot pick it.
func (c collector) pin(dir string, id federate.HostIdentity) error {
	path, err := pinsPath(c.e, c.db)
	if err != nil {
		return err
	}
	pins, err := loadHostPins(path)
	if err != nil {
		return err
	}
	return pinNewHosts(path, pins, map[string]federate.HostIdentity{dir: id})
}

// pinsPath is the pins file beside the service database db, the default
// database when db is empty: where build reads it.
func pinsPath(e env, db string) (string, error) {
	if db == "" {
		var err error
		if db, err = e.servicedb(); err != nil {
			return "", err
		}
	}
	return filepath.Join(filepath.Dir(db), hostPinsFileName), nil
}

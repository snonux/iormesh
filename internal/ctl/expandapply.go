package ctl

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"iormesh/internal/expand"
	"iormesh/internal/federate"
	"iormesh/internal/termsafe"
)

// `iormeshctl expand -apply` (PLAN.md M6 "Apply", "Rounds"): one
// expansion round. The plan expand prints is acted on, in this order:
//
//  1. expand.Enroll adds the candidates to the registry in memory — one
//     host per address, written as that address, one project tracing
//     whatever listens on the peer's port, its hop recorded — after
//     checking each against the allowlist and limits once more. Peers the
//     plan skipped (outside the allowlist, past -hops or max_nodes,
//     enrolled already, the controller's own traffic) are never touched.
//  2. The pinned key and the deploy record are read, so a controller
//     that cannot deploy fails before the registry changes.
//  3. The registry is saved: deploy reaches only registered hosts
//     (principle #3), and expansion is no exception — the file is the
//     record of what the operator's key was pointed at.
//  4. Per new host, over the same Transport: its /etc/machine-id (the
//     host_id its tracer would stamp) is read unprivileged; a machine the
//     store already holds traces of, reached at another address, is
//     unregistered again and nothing is deployed. Otherwise deploy's own
//     steps run unchanged (deploy.go): preflight, verify, stage, upgrade
//     and start through the one sudoers rule — no new sudo path.
//
// A round is one invocation. The next comes after traffic flowed and a
// refresh: the new hosts' traces then reveal the next ring, the hops
// recorded in the registry keep counting from the hosts enrolled by hand,
// and the visited set (registered literals, addresses traced hosts own)
// is what makes a cycle end — a round that finds nothing new enrolls
// nothing, which is how expansion terminates.

// applyExpansion enrolls and deploys x's candidates.
func applyExpansion(ctx context.Context, e env, f expandFlags, x expansion) error {
	enrolled, skipped, err := expand.Enroll(&x.reg, x.proposal, x.allow)
	if err != nil {
		return err
	}
	for _, s := range skipped {
		_, _ = fmt.Fprintf(e.stdout, "not enrolled %s:%d: %s: %s\n", s.Addr, s.Port, s.Reason,
			termsafe.String(s.Evidence))
	}
	if len(enrolled) == 0 {
		_, err := fmt.Fprintln(e.stdout, "expand: nothing to enroll; the mesh is complete within the limits")
		return err
	}
	d, recPath, err := newDeployer(e, f.dist, f.key, false)
	if err != nil {
		return fmt.Errorf("%w; nothing enrolled", err)
	}
	if err := x.reg.Save(x.regPath); err != nil {
		return err
	}
	servers := make([]federate.Server, len(enrolled))
	for i, n := range enrolled {
		servers[i] = n.Server
		p := n.Server.Projects[0]
		_, _ = fmt.Fprintf(e.stdout, "enrolled %s hop %d project %s listen-port %d (%s)\n", n.Server.Host,
			n.Server.Hop, p.Name, p.Target.ListenPort, x.regPath)
	}
	a := &applier{d: d, known: x.hostIDs}
	deployErr := d.all(ctx, servers, recPath, a.host)
	if err := a.unregisterDuplicates(&x.reg, x.regPath); err != nil {
		return errors.Join(deployErr, err)
	}
	if deployErr != nil {
		return fmt.Errorf("%w (failed hosts stay registered: iormeshctl deploy HOST retries, "+
			"iormeshctl rm HOST removes)", deployErr)
	}
	return nil
}

// applier deploys to newly enrolled hosts, refusing a machine the store
// already traces.
type applier struct {
	d deployer
	// known maps host_ids the store holds to their directories.
	known map[string]string
	// dups are the enrolled hosts found to be known machines.
	dups []string
}

// host checks srv's machine identity, then runs deploy's steps on it.
// Reading /etc/machine-id needs no privilege and changes nothing on the
// host; it is the expand.Plan residual (a host registered by name whose
// other address a trace reached) caught before a second tracer lands on
// one machine.
func (a *applier) host(ctx context.Context, srv federate.Server) error {
	t, err := a.d.e.transport(srv.Host)
	if err != nil {
		return err
	}
	out, err := t.Run(ctx, "cat", "/etc/machine-id")
	if err != nil {
		return fmt.Errorf("machine id: %w", err)
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return errors.New("machine id: /etc/machine-id is empty, nothing deployed")
	}
	if dir, ok := a.known[id]; ok {
		a.dups = append(a.dups, srv.Host)
		return fmt.Errorf("same machine as traced host %s (machine-id %s): unregistered again, "+
			"nothing deployed", dir, id)
	}
	return a.d.host(ctx, srv)
}

// unregisterDuplicates removes the hosts host found to be known machines
// from the registry and saves it.
func (a *applier) unregisterDuplicates(reg *federate.Registry, path string) error {
	if len(a.dups) == 0 {
		return nil
	}
	for _, h := range a.dups {
		if err := reg.Remove(h, ""); err != nil {
			return err
		}
	}
	return reg.Save(path)
}

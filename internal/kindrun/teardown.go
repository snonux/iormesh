package kindrun

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

// TeardownPlan is every removal, in order: the cluster (or, in an
// existing cluster, what the record o names: the run's Traefik and
// namespaces, nothing without a record), the registry container with
// its anonymous volumes and its named data volume, the run's own image
// tags, any image left carrying the run's label, and the base images
// the run pulled (by ID: Pulled, those its setup used that were absent
// before). Nothing else is named, and no prune runs without the run's
// label filter.
func TeardownPlan(c Config, o *Owned, pulled []string) [][]string {
	plan := append(clusterTeardown(c, o),
		docker("rm", "--force", "--volumes", c.Registry),
		docker("volume", "rm", "--force", c.RegistryVolume))
	for _, img := range c.Images {
		plan = append(plan, docker("image", "rm", "--force", img))
	}
	plan = append(plan, docker("image", "prune", "--all", "--force", "--filter=label="+c.Label))
	for _, id := range pulled {
		plan = append(plan, docker("image", "rm", id))
	}
	return plan
}

// Teardown runs TeardownPlan and removes the state file and, unless the
// cluster is an existing one, the kubeconfig the run wrote. A failing
// step is reported on w and the rest still run (a missing cluster or
// volume is the normal case of a repeat); the error joins the failures
// of the steps that matter — the listing, an existing cluster's
// namespace deletion and the file removals — not those of removals with
// nothing to remove. When that namespace deletion fails the state file
// stays, so the record of what is the run's survives for the next
// `mage k8sTeardown` (task li4); it also stays, with nothing removed in
// the cluster, for a refused run (LeaveCluster) and for a record naming
// no kubeconfig or context (clusterRecord).
func Teardown(r Runner, c Config, w io.Writer) error {
	before, known, err := c.Load()
	if err != nil {
		return err
	}
	now, serr := Snapshot(r, c)
	if serr != nil {
		_, _ = fmt.Fprintf(w, "teardown: %v; base images kept\n", serr)
		known = false
	}
	if !known {
		_, _ = fmt.Fprintln(w, "teardown: no record of the base images before the run: none removed")
	}
	owned, notice, keep, oerr := clusterRecord(c, before.Owned)
	if notice != "" {
		_, _ = fmt.Fprintln(w, "teardown: "+notice)
	}
	failed := runPlan(r, TeardownPlan(c, owned, Pulled(before, known, now)), w)
	var kerr error
	if del := namespaceDelete(owned); del != nil && slices.ContainsFunc(failed,
		func(a []string) bool { return slices.Equal(a, del) }) {
		kerr = fmt.Errorf("namespaces %s not deleted: record kept in %s",
			strings.Join(owned.Namespaces, " "), c.StateFile)
	}
	return errors.Join(serr, oerr, kerr, removeFiles(c, keep || kerr != nil))
}

// runPlan runs every step of plan, reports each failure on w, and
// returns the failed steps.
func runPlan(r Runner, plan [][]string, w io.Writer) [][]string {
	var failed [][]string
	for _, argv := range plan {
		if err := r.Run(argv...); err != nil {
			_, _ = fmt.Fprintf(w, "teardown: %s: %v\n", strings.Join(argv, " "), err)
			failed = append(failed, argv)
		}
	}
	return failed
}

// removeFiles removes ownedFiles, the state file only unless keepState.
func removeFiles(c Config, keepState bool) error {
	var errs []error
	for _, f := range ownedFiles(c) {
		if keepState && f == c.StateFile {
			continue
		}
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

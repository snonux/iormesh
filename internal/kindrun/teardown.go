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
// step is reported on w and the rest still run; the error joins the
// listing's failure, the failed steps that matter (stepsError: all but
// helm's uninstall, since the steps exit 0 when their object is already
// gone) and the file removals'. When such a step fails the state file
// stays, so the record of what is the run's — the pulled base images,
// an existing cluster's namespaces — survives for the next `mage
// k8sTeardown` (tasks li4, qi4); it also stays, with nothing removed in
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
	ferr := stepsError(c, owned, failed)
	return errors.Join(serr, oerr, ferr, removeFiles(c, keep || ferr != nil))
}

// stepsError is the error of the failed steps that matter, nil when
// none: every step but helm's uninstall, which is excused — the
// release's storage is in namespace traefik and goes with the namespace
// deletion, whose own failure is reported (task li4). Every other step
// exits 0 when its object is already gone (`kind delete cluster` on a
// missing cluster, `docker rm --force`, `docker volume rm --force`,
// `docker image rm --force`, a label-filtered prune, `kubectl delete
// --ignore-not-found`; the unforced `docker image rm <id>` names only
// IDs Snapshot listed just before), so its failure is a real one — sudo,
// the daemon or the cluster refusing — and not the repeat of a
// teardown; the state file is then kept for the next `mage k8sTeardown`
// (task qi4). Runner.Run streams the output and returns only the exit,
// so no message is classified.
func stepsError(c Config, owned *Owned, failed [][]string) error {
	var errs []error
	del := namespaceDelete(owned)
	for _, argv := range failed {
		switch {
		case argv[0] == "helm":
		case del != nil && slices.Equal(argv, del):
			errs = append(errs, fmt.Errorf("namespaces %s not deleted",
				strings.Join(owned.Namespaces, " ")))
		default:
			errs = append(errs, fmt.Errorf("%s failed", strings.Join(argv, " ")))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%w: record kept in %s", errors.Join(errs...), c.StateFile)
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

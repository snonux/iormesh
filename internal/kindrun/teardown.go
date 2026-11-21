package kindrun

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// TeardownPlan is every removal, in order: the cluster, the registry
// container with its anonymous volumes and its named data volume, the
// run's own image tags, any image left carrying the run's label, and
// the base images the run pulled (by ID). Nothing else is named, and no
// prune runs without the run's label filter.
func TeardownPlan(c Config, pulled []string) [][]string {
	plan := [][]string{
		kind("delete", "cluster", "--name="+c.Cluster),
		docker("rm", "--force", "--volumes", c.Registry),
		docker("volume", "rm", "--force", c.RegistryVolume),
	}
	for _, img := range c.Images {
		plan = append(plan, docker("image", "rm", "--force", img))
	}
	plan = append(plan, docker("image", "prune", "--all", "--force", "--filter=label="+c.Label))
	for _, id := range pulled {
		plan = append(plan, docker("image", "rm", id))
	}
	return plan
}

// Teardown runs TeardownPlan and removes the kubeconfig and the state
// file. A failing step is reported on w and the rest still run (a
// missing cluster or volume is the normal case of a repeat); the error
// joins the failures of the steps that matter — the listing and the file
// removals — not those of removals with nothing to remove.
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
	for _, argv := range TeardownPlan(c, Pulled(before, known, now)) {
		if err := r.Run(argv...); err != nil {
			_, _ = fmt.Fprintf(w, "teardown: %s: %v\n", strings.Join(argv, " "), err)
		}
	}
	var errs []error
	for _, f := range []string{c.Kubeconfig, c.StateFile} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(serr, errors.Join(errs...))
}

// Package kindrun holds the command plans of M7's kind acceptance run
// (task yb4, `mage k8sAcceptance` and `mage k8sTeardown`,
// docs/testing.md "M7 acceptance on kind"): the local registry, the
// cluster's kubeconfig, the image builds, the disk guard and, above all,
// a teardown scoped to what the run itself made. The magefile only wires
// a Runner that executes argv; the plans are unit-tested with a fake.
//
// Never `docker system prune`, nor any prune without the run's label
// filter: the Docker daemon is the operator's, and it holds images,
// volumes and build cache that are not this run's (review N7, task yb4:
// a system prune removed the operator's unrelated images).
package kindrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

// Runner runs one argv. Run streams its output; Output returns stdout.
type Runner interface {
	Run(argv ...string) error
	Output(argv ...string) (string, error)
}

// Config names everything the run creates.
type Config struct {
	Cluster, Context, Node string
	// Registry is the registry container, RegistryVolume its named data
	// volume, RegistryImage the pinned image it runs.
	Registry, RegistryVolume, RegistryImage string
	// Label marks every image the run builds (key=value).
	Label string
	// Images are the run's own tags.
	Images []string
	// BaseRepos are the repositories of the base images the run may
	// pull: kind's node image, the registry, the workload's JRE. Snapshot
	// lists them before the run; they only say what was there, never
	// what to remove (task oi4).
	BaseRepos []string
	// JREImage is the workload's base image exactly as
	// scripts/container/kind-workload.Containerfile pins it (a test
	// keeps the two equal); UsedImages resolves it to the ID the build
	// used.
	JREImage string
	// Kubeconfig is the operator's kubeconfig file for the cluster;
	// StateFile records the base images present before the run and
	// those the run used (State).
	Kubeconfig, StateFile string
	// Existing marks a cluster the run did not create (Existing, task
	// zg4): no kind cluster is made or deleted and the kubeconfig stays.
	// Namespaces are those the run creates there; ClaimExisting refuses
	// when any is already there and records them, and the teardown
	// deletes only the recorded ones (task li4).
	Existing   bool
	Namespaces []string
	// LeaveCluster makes the teardown remove nothing in the cluster and
	// keep the state file: set for a run that ClaimExisting refused with
	// ErrForeignRecord, whose record names objects of another run.
	LeaveCluster bool
}

// RegistryAt is where the node and the host reach the local registry.
const RegistryAt = "localhost:5001"

// Default is the acceptance run's configuration; kubeconfig and state
// are file paths the caller chooses (the repository root by default).
func Default(kubeconfig, state string) Config {
	return Config{Cluster: "iormesh-m7", Context: "kind-iormesh-m7", Node: "iormesh-m7-control-plane",
		Registry: "iormesh-registry", RegistryVolume: "iormesh-registry-data",
		RegistryImage: "docker.io/library/registry:2@sha256:" +
			"a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373",
		Label:     "org.iormesh.acceptance=m7",
		Images:    []string{RegistryAt + "/iormesh/tracer:m7", RegistryAt + "/iormesh/workload:m7"},
		BaseRepos: []string{"kindest/node", "registry", "eclipse-temurin"},
		JREImage: "docker.io/library/eclipse-temurin:25-jre@sha256:" +
			"4ed132f6832e9804b123e89a849a7b4502756ae64a4ee08ebab939d9d5d0a9fa",
		Kubeconfig: kubeconfig, StateFile: state}
}

// docker is an argv of docker through sudo -n, as every docker call of
// the run (nobody joins the docker group).
func docker(args ...string) []string { return append([]string{"sudo", "-n", "docker"}, args...) }

// kind is the same for kind, which drives docker.
func kind(args ...string) []string { return append([]string{"sudo", "-n", "kind"}, args...) }

// State is what was there before the run — per base repository, the
// image IDs present — the exact base image IDs the run's setup used
// (Used, added by RecordUsed), and, in an existing cluster, what the run
// created there (Owned, written by ClaimExisting; nil until then and on
// kind).
type State struct {
	BaseImages map[string][]string `json:"base_images"`
	Used       []string            `json:"used_images,omitempty"`
	Owned      *Owned              `json:"owned,omitempty"`
}

// Snapshot lists the base images present now.
func Snapshot(r Runner, c Config) (State, error) {
	s := State{BaseImages: map[string][]string{}}
	for _, repo := range c.BaseRepos {
		out, err := r.Output(docker("image", "ls", "--no-trunc", "--filter=reference="+repo,
			"--format={{.ID}}")...)
		if err != nil {
			return State{}, fmt.Errorf("list %s images: %w", repo, err)
		}
		s.BaseImages[repo] = strings.Fields(out)
	}
	return s, nil
}

// Save writes s to the state file (0600) unless one is there already: a
// rerun over a kept cluster must not forget what preceded the first run.
// It is never refreshed, so it alone must not decide removals: an image
// pulled into a base repository after it was written is not the run's
// unless the run also used it (RecordUsed, Pulled; task oi4).
func (c Config) Save(s State) error {
	if _, err := os.Stat(c.StateFile); err == nil {
		return nil
	}
	return c.write(s)
}

// write writes s to the state file (0600), replacing what is there.
func (c Config) write(s State) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(c.StateFile, data, 0o600)
}

// Load reads the state file; without one it is the zero State, in which
// no base image counts as the run's — teardown then keeps them all.
func (c Config) Load() (State, bool, error) {
	data, err := os.ReadFile(c.StateFile)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, false, fmt.Errorf("%s: %w", c.StateFile, err)
	}
	return s, true, nil
}

// UsedImages resolves the base images the run's setup used to their
// IDs: kind's node image (the node container's, not on an existing
// cluster), the registry's (its container's) and the JRE the workload
// build pulled (c.JREImage). One that cannot be resolved — not created,
// not pulled — is left out: an image the run never got is not its own.
func UsedImages(r Runner, c Config) []string {
	argvs := [][]string{
		docker("container", "inspect", "--format={{.Image}}", c.Registry),
		docker("image", "inspect", "--format={{.Id}}", c.JREImage),
	}
	if !c.Existing {
		argvs = append(argvs, docker("container", "inspect", "--format={{.Image}}", c.Node))
	}
	var ids []string
	for _, argv := range argvs {
		if out, err := r.Output(argv...); err == nil && strings.HasPrefix(strings.TrimSpace(out), "sha256:") {
			ids = append(ids, strings.TrimSpace(out))
		}
	}
	return ids
}

// RecordUsed adds ids to the state file's Used, keeping what an earlier
// run over a kept cluster recorded. Without a state file it records
// nothing: an ID is only the run's when the file also says it was
// absent before, and Save writes that first.
func (c Config) RecordUsed(ids []string) error {
	s, known, err := c.Load()
	if err != nil || !known {
		return err
	}
	used := append(slices.Clone(s.Used), ids...)
	slices.Sort(used)
	s.Used = slices.Compact(used)
	return c.write(s)
}

// Pulled is what the teardown removes of the base images: the IDs the
// run recorded as used (before.Used) that no base repository held
// before the run and that are still there now. An image that appeared
// in a base repository since, but which the run did not use (the
// operator's own pull for other work), is not among them (task oi4).
// With no recorded state nothing counts as pulled.
func Pulled(before State, known bool, now State) []string {
	if !known {
		return nil
	}
	held := func(s State, id string) bool {
		for _, ids := range s.BaseImages {
			if slices.Contains(ids, id) {
				return true
			}
		}
		return false
	}
	var ids []string
	for _, id := range before.Used {
		if !held(before, id) && held(now, id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

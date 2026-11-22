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
	// BaseRepos are the base images the run may pull: kind's node image,
	// the registry, the workload's JRE. Teardown removes only the IDs of
	// these that were not there before the run (State).
	BaseRepos []string
	// Kubeconfig is the operator's kubeconfig file for the cluster;
	// StateFile records the base images present before the run.
	Kubeconfig, StateFile string
	// Existing marks a cluster the run did not create (Existing, task
	// zg4): no kind cluster is made or deleted and the kubeconfig stays.
	// Namespaces are those the run creates there; ClaimExisting refuses
	// when any is already there and records them, and the teardown
	// deletes only the recorded ones (task li4).
	Existing   bool
	Namespaces []string
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
		Label:      "org.iormesh.acceptance=m7",
		Images:     []string{RegistryAt + "/iormesh/tracer:m7", RegistryAt + "/iormesh/workload:m7"},
		BaseRepos:  []string{"kindest/node", "registry", "eclipse-temurin"},
		Kubeconfig: kubeconfig, StateFile: state}
}

// docker is an argv of docker through sudo -n, as every docker call of
// the run (nobody joins the docker group).
func docker(args ...string) []string { return append([]string{"sudo", "-n", "docker"}, args...) }

// kind is the same for kind, which drives docker.
func kind(args ...string) []string { return append([]string{"sudo", "-n", "kind"}, args...) }

// State is what was there before the run — per base repository, the
// image IDs present — and, in an existing cluster, what the run created
// there (Owned, written by ClaimExisting; nil until then and on kind).
type State struct {
	BaseImages map[string][]string `json:"base_images"`
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

// Pulled is, per base repository, the IDs in now that were not in
// before; with no recorded state nothing counts as pulled.
func Pulled(before State, known bool, now State) []string {
	if !known {
		return nil
	}
	var ids []string
	for repo, cur := range now.BaseImages {
		for _, id := range cur {
			if !slices.Contains(before.BaseImages[repo], id) {
				ids = append(ids, id)
			}
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

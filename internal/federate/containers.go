package federate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"iormesh/internal/k8sname"
)

// Container map (PLAN.md M7 "Pod fields on events", task jk4): an
// in-pod tracer's events carry the runtime id of a process's container
// (pod.container_id), never its name — the tracer has no API access.
// Collect, which asks the API server for the pod anyway, stores the
// pod's container id → name map in the pod's store directory,
// <store>/<project>/pod-<uid>/containers.json, beside its segments, so
// a build names containers offline and the same store always builds the
// same names.

// ContainersFile is a pod's container map in its store directory: a
// JSON object {"<64-hex runtime id>": "<container name>"}. The store
// listing skips it (listHost): it is no segment and no stray entry.
const ContainersFile = "containers.json"

// maxContainersSize bounds the map read: a pod has a few containers and
// each restart adds one id, so a megabyte is far past any real one.
const maxContainersSize = 1 << 20

// ReadContainers reads the container map in host directory dir; a
// missing file is an empty map. A file over maxContainersSize, not a
// JSON object, or holding an id that is no runtime id
// (k8sname.RuntimeID) or a name that is no container name (an RFC 1123
// label) is an error: the whole file is then distrusted, never half
// read.
func ReadContainers(dir string) (map[string]string, error) {
	path := filepath.Join(dir, ContainersFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("container map: %w", err)
	}
	if len(data) > maxContainersSize {
		return nil, fmt.Errorf("container map: %s is over %d bytes", path, maxContainersSize)
	}
	m := map[string]string{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("container map: %s: %w", path, err)
	}
	if err := ValidContainers(m); err != nil {
		return nil, fmt.Errorf("container map: %s: %w", path, err)
	}
	return m, nil
}

// ValidContainers checks every entry of a container map: a runtime id
// to a container name.
func ValidContainers(m map[string]string) error {
	for id, name := range m {
		if !k8sname.RuntimeID(id) {
			return fmt.Errorf("%q is not a 64-hex-digit runtime id", id)
		}
		if !k8sname.Label(name) {
			return fmt.Errorf("container %s: %q is not a container name", id, name)
		}
	}
	return nil
}

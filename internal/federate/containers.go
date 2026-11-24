package federate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

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

// maxContainersSize bounds the map read (readBounded reads at most one
// byte past it): a pod has a few containers and each restart adds one
// id, so a megabyte is far past any real one.
const maxContainersSize = 1 << 20

// ReadContainers reads the container map in host directory dir; a
// missing file, or a JSON null, is an empty map, never nil. A store
// entry that is no regular file (a symlink is not followed, a FIFO not
// waited on), a file over maxContainersSize, one that is not a JSON
// object, or one holding an id that is no runtime id
// (k8sname.RuntimeID) or a name that is no container name (an RFC 1123
// label) is an error: the whole file is then distrusted, never half
// read. The error says why without the file's path, which the caller
// names as it shows it (the build's health holds no local paths).
func ReadContainers(dir string) (map[string]string, error) {
	data, err := readBounded(filepath.Join(dir, ContainersFile))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if err := ValidContainers(m); err != nil {
		return nil, err
	}
	if m == nil { // the file held null
		m = map[string]string{}
	}
	return m, nil
}

// readBounded reads the regular file at path, refusing anything else
// before a read could block or follow a link — O_NOFOLLOW, O_NONBLOCK
// (a FIFO's open returns at once), then fstat — and anything over
// maxContainersSize without reading past it. A missing file is
// fs.ErrNotExist; errors carry no path.
func readBounded(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		if errors.Is(err, syscall.ELOOP) {
			return nil, errors.New("a symlink, not a regular file")
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxContainersSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxContainersSize {
		return nil, fmt.Errorf("over %d bytes", maxContainersSize)
	}
	return data, nil
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

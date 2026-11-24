package ctl

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"iormesh/internal/federate"
)

// Container maps (docs/milestones.md M7 "Pod fields on events", task jk4):
// collect records each pod's container id → name map, which the API server
// reports, in the pod's store directory of every project it collects
// (federate.ContainersFile); build reads it back to name the container
// each process's pod.container_id points at, offline.

// recordContainers merges a pod's container map into the one in its
// store directory dir and writes the file (0600, atomically) when
// anything was added. A recorded id keeps its first name: runtime ids
// are unique, and the map only grows, so an id from a run before a
// restart stays named after the pod status forgot it. Two collects of
// one pod at once may each write: the later rename wins, and what it
// lacks the next collect adds again.
//
// A map already there that does not read is left as it is and the merge
// refused, the error saying how to recover: collect does not rewrite it
// from the current status on its own, since that would drop the names
// of ids the status no longer lists, and the operator should see a file
// that went bad (a hand edit, a planted FIFO or symlink) rather than
// have it replaced quietly.
func recordContainers(dir string, seen map[string]string) error {
	if err := federate.ValidContainers(seen); err != nil {
		return fmt.Errorf("container map: %w", err)
	}
	m, err := federate.ReadContainers(dir)
	if err != nil {
		path := filepath.Join(dir, federate.ContainersFile)
		return fmt.Errorf("container map %s: %w; not updated: remove it, and the next collect writes it anew "+
			"from the pod's current status (containers no longer in it then show their ids)", path, err)
	}
	changed := false
	for id, name := range seen {
		if _, ok := m[id]; !ok {
			m[id], changed = name, true
		}
	}
	if !changed {
		return nil
	}
	data, err := json.MarshalIndent(m, "", "  ") // sorted keys: the same map, the same bytes
	if err != nil {
		return fmt.Errorf("container map: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(dir, federate.ContainersFile), append(data, '\n')); err != nil {
		return fmt.Errorf("container map: %w", err)
	}
	return nil
}

// containerMaps reads the container map of every host directory the
// build's segments come from, keyed by host directory (what
// viewmodel.Input.Containers takes): a directory under several projects
// is one pod, so its maps merge. A map that does not read is named in
// the returned list, as a store entry the build ignored, with why
// (ReadContainers' error, which holds no local path), and names
// nothing — its processes show their ids.
func containerMaps(store string, segs federate.Segments) (map[string]map[string]string, []string) {
	out := map[string]map[string]string{}
	var bad []string
	read := map[[2]string]bool{}
	for _, s := range segs.Sources {
		key := [2]string{s.Project, s.Host}
		if read[key] {
			continue
		}
		read[key] = true
		m, err := federate.ReadContainers(filepath.Join(store, s.Project, s.Host))
		if err != nil {
			bad = append(bad, filepath.Join(s.Project, s.Host, federate.ContainersFile)+": not a container map: "+
				err.Error())
			continue
		}
		if len(m) == 0 {
			continue
		}
		if out[s.Host] == nil {
			out[s.Host] = map[string]string{}
		}
		for id, name := range m {
			out[s.Host][id] = name
		}
	}
	return out, bad
}

package cli

import (
	"io"
	"os"
	"path/filepath"
	"strconv"

	"iormesh/internal/correlate"
	"iormesh/internal/k8s"
)

// maxCgroupFile bounds one /proc/<pid>/cgroup read. The kernel writes a
// line per hierarchy (one on cgroup v2, a dozen on v1), each a path of
// at most PATH_MAX; 64 KiB is far above any real file.
const maxCgroupFile = 64 << 10

// podOf is the pod a run is in, from the downward API (k8s.PodFromEnv),
// nil outside a pod. describeHost has refused a partial or malformed set
// already, so an error here only means "not a pod".
func podOf(getenv func(string) string) *k8s.PodRef {
	pod, ok, err := k8s.PodFromEnv(getenv)
	if !ok || err != nil {
		return nil
	}
	return &pod
}

// podConfig gives an in-pod run's correlator the pod fields of its events
// (docs/milestones.md M7 "Pod fields on events", task sh4): the pod's
// namespace and name, and a reader of each process's container from its cgroup
// under the tracer's own /proc (its PID namespace, which the pod shares, so
// event pids name the right entries), asked once per process. Outside a
// pod it leaves
// cfg alone: no pod object, no cgroup read.
func podConfig(cfg *correlate.Config, pod *k8s.PodRef) {
	if pod == nil {
		return
	}
	cfg.Pod = &correlate.PodConfig{Namespace: pod.Namespace, Name: pod.Name}
	cfg.ContainerOf = cgroupContainer("/proc", pod.UID)
}

// cgroupContainer returns the container reader for the pod podUID:
// /proc/<pid>/cgroup under procRoot, bounded, through k8s.ContainerOf. A
// process gone by the time it is read, or any read error, names no
// container: the field is enrichment, and the event is written anyway.
func cgroupContainer(procRoot, podUID string) func(pid uint32) string {
	return func(pid uint32) string {
		f, err := os.Open(filepath.Join(procRoot, strconv.FormatUint(uint64(pid), 10), "cgroup"))
		if err != nil {
			return ""
		}
		defer func() { _ = f.Close() }() // read-only: nothing to lose
		data, err := io.ReadAll(io.LimitReader(f, maxCgroupFile))
		if err != nil {
			return ""
		}
		return k8s.ContainerOf(data, podUID)
	}
}

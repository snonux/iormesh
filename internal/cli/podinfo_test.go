package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"iormesh/internal/k8s"
)

// TestCgroupContainer: the run's container reader (task sh4) reads
// /proc/<pid>/cgroup under its proc root and names the container of the
// tracer's pod; a process of another pod, the tracer's own "0::/" and a
// vanished pid name none.
func TestCgroupContainer(t *testing.T) {
	const uid = "3b1c9a52-7e0d-4c1f-9a8e-2f6d5b4c3a21"
	ctr := strings.Repeat("c3", 32)
	root := t.TempDir()
	for pid, data := range map[string]string{
		"10": "0::/../cri-containerd-" + ctr + ".scope\n",
		"11": "0::/kubepods.slice/kubepods-pod3b1c9a52_7e0d_4c1f_9a8e_2f6d5b4c3a21.slice/cri-containerd-" +
			ctr + ".scope\n",
		"12": "0::/kubepods.slice/kubepods-pod00000000_7e0d_4c1f_9a8e_2f6d5b4c3a21.slice/cri-containerd-" +
			ctr + ".scope\n",
		"13": "0::/\n",
	} {
		if err := os.MkdirAll(filepath.Join(root, pid), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, pid, "cgroup"), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	of := cgroupContainer(root, uid)
	for pid, want := range map[uint32]string{10: ctr, 11: ctr, 12: "", 13: "", 14: ""} {
		if got := of(pid); got != want {
			t.Errorf("pid %d: %q, want %q", pid, got, want)
		}
	}
}

// TestCorrelatorConfigInPod: a run in a pod gives the correlator the pod
// and a container reader; outside a pod it gets neither.
func TestCorrelatorConfigInPod(t *testing.T) {
	pod := &k8s.PodRef{Name: "web-7d9f", Namespace: "prod", UID: "3b1c9a52-7e0d-4c1f-9a8e-2f6d5b4c3a21"}
	cfg := correlatorConfig(runConfig{project: "p"}, outputs{hostID: pod.HostID(), pod: pod}, &failSink{}, 0)
	if cfg.Pod == nil || cfg.Pod.Namespace != "prod" || cfg.Pod.Name != "web-7d9f" || cfg.ContainerOf == nil {
		t.Errorf("in a pod: Pod %+v, ContainerOf set %v", cfg.Pod, cfg.ContainerOf != nil)
	}
	cfg = correlatorConfig(runConfig{project: "p"}, outputs{hostID: "h"}, &failSink{}, 0)
	if cfg.Pod != nil || cfg.ContainerOf != nil {
		t.Errorf("outside a pod: Pod %+v, ContainerOf set %v", cfg.Pod, cfg.ContainerOf != nil)
	}
}

package k8s

import "testing"

// /proc/<pid>/cgroup shapes on k3s on el9 (Rocky 9, cgroup v2, k3s's
// embedded containerd under the systemd cgroup driver, and under
// cgroupfs for a k3s set up that way), as a node-level reader and as an
// in-pod tracer's private cgroup namespace show them. Constructed by the
// kubelet's and containerd's formats, not captured (task sh4; kind's
// captures are TestParseProcCgroupKindCaptures').
const (
	k3sUID       = "5d0e7b44-1c2a-4f3e-8b9d-0a1b2c3d4e5f"
	k3sAppCtr    = "0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9"
	k3sSystemd   = "0::/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod5d0e7b44_1c2a_4f3e_8b9d_0a1b2c3d4e5f.slice/cri-containerd-" + k3sAppCtr + ".scope\n"
	k3sCgroupfs  = "0::/kubepods/burstable/pod" + k3sUID + "/" + k3sAppCtr + "\n"
	k3sRelSystem = "0::/../cri-containerd-" + k3sAppCtr + ".scope\n"
	k3sRelCgfs   = "0::/../" + k3sAppCtr + "\n"
	k3sPodItself = "0::/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod5d0e7b44_1c2a_4f3e_8b9d_0a1b2c3d4e5f.slice\n"
)

// TestContainerOfShapes: ContainerOf names the container of a process of
// the tracer's pod in every shape a tracer meets — kind's captures and
// k3s on el9's systemd and cgroupfs paths, full or relative to the
// tracer's own cgroup namespace — and names none for the tracer's own
// container ("0::/"), the pod cgroup itself, another pod's container, a
// host process, or a file that is not the kernel's.
func TestContainerOfShapes(t *testing.T) {
	for name, c := range map[string]struct {
		data, pod, want string
	}{
		"kind app in node":        {kindInNode, kindPodUID, kindAppCtr},
		"kind app from host":      {kindFromHost, kindPodUID, kindAppCtr},
		"kind tracer from app":    {kindDbgFromApp, kindPodUID, kindDbgCtr},
		"k3s systemd":             {k3sSystemd, k3sUID, k3sAppCtr},
		"k3s cgroupfs":            {k3sCgroupfs, k3sUID, k3sAppCtr},
		"k3s relative systemd":    {k3sRelSystem, k3sUID, k3sAppCtr},
		"k3s relative cgroupfs":   {k3sRelCgfs, k3sUID, k3sAppCtr},
		"own container":           {kindAppOwnView, kindPodUID, ""},
		"pod cgroup itself":       {k3sPodItself, k3sUID, ""},
		"another pod":             {k3sSystemd, kindPodUID, ""},
		"host process":            {"0::/system.slice/sshd.service\n", k3sUID, ""},
		"climbs out of the pod":   {"0::/../../kubepods-besteffort.slice\n", k3sUID, ""},
		"not the kernel's":        {"garbage\n", k3sUID, ""},
		"empty (process is gone)": {"", k3sUID, ""},
	} {
		if got := ContainerOf([]byte(c.data), c.pod); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

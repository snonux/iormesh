package k8s

import (
	"errors"
	"strings"
	"testing"
)

// Ids in the kubelet's and the runtimes' real formats: a dashed pod UID,
// its systemd-escaped form, a static pod's hash UID, and 64-hex
// container ids.
const (
	uid       = "3b1c9a52-7e0d-4c1f-9a8e-2f6d5b4c3a21"
	uidEsc    = "3b1c9a52_7e0d_4c1f_9a8e_2f6d5b4c3a21"
	staticPod = "9f2c4e7a1b3d5f6e8a0c2e4f6a8b0d1c"
	ctr       = "4f6c0f1e0a3d2b8c9e7f5a6b4c3d2e1f0a9b8c7d6e5f4a3b2c1d0e9f8a7b6c5d"
	nodeID    = "b6a3a5e9f1d2c4b7a8e0f3d6c9b2a5e8f1d4c7b0a3e6f9d2c5b8a1e4f7d0c3b6"
)

// TestParseCgroupPathRuntimes covers both kubelet cgroup drivers for
// containerd and CRI-O (and Docker through cri-dockerd), every QoS
// class, kind's nesting, and a static pod.
func TestParseCgroupPathRuntimes(t *testing.T) {
	cases := map[string]struct {
		path string
		want CgroupIdentity
	}{
		"containerd systemd burstable": {
			"/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod" + uidEsc + ".slice/cri-containerd-" + ctr + ".scope",
			CgroupIdentity{uid, ctr, RuntimeContainerd, QoSBurstable}},
		"containerd systemd guaranteed": {
			"/kubepods.slice/kubepods-pod" + uidEsc + ".slice/cri-containerd-" + ctr + ".scope",
			CgroupIdentity{uid, ctr, RuntimeContainerd, QoSGuaranteed}},
		"crio systemd besteffort": {
			"/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-pod" + uidEsc + ".slice/crio-" + ctr + ".scope",
			CgroupIdentity{uid, ctr, RuntimeCRIO, QoSBestEffort}},
		"crio systemd container subcgroup": {
			"/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod" + uidEsc + ".slice/crio-" + ctr + ".scope/container",
			CgroupIdentity{uid, ctr, RuntimeCRIO, QoSBurstable}},
		"docker systemd": {
			"/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod" + uidEsc + ".slice/docker-" + ctr + ".scope",
			CgroupIdentity{uid, ctr, RuntimeDocker, QoSBurstable}},
		"containerd cgroupfs burstable": {
			"/kubepods/burstable/pod" + uid + "/" + ctr,
			CgroupIdentity{uid, ctr, RuntimeUnknown, QoSBurstable}},
		"containerd cgroupfs guaranteed": {
			"/kubepods/pod" + uid + "/" + ctr,
			CgroupIdentity{uid, ctr, RuntimeUnknown, QoSGuaranteed}},
		"crio cgroupfs besteffort": {
			"/kubepods/besteffort/pod" + uid + "/crio-" + ctr,
			CgroupIdentity{uid, ctr, RuntimeCRIO, QoSBestEffort}},
		// kind runs the kubelet with --cgroup-root=/kubelet; inside the
		// node's cgroup namespace the path starts there...
		"kind in node": {
			"/kubelet.slice/kubelet-kubepods.slice/kubelet-kubepods-besteffort.slice/kubelet-kubepods-besteffort-pod" +
				uidEsc + ".slice/cri-containerd-" + ctr + ".scope",
			CgroupIdentity{uid, ctr, RuntimeContainerd, QoSBestEffort}},
		// ...and seen from the host it sits under the node container.
		"kind from host": {
			"/system.slice/docker-" + nodeID + ".scope/kubelet.slice/kubelet-kubepods.slice/kubelet-kubepods-burstable.slice/" +
				"kubelet-kubepods-burstable-pod" + uidEsc + ".slice/cri-containerd-" + ctr + ".scope",
			CgroupIdentity{uid, ctr, RuntimeContainerd, QoSBurstable}},
		"static pod": {
			"/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod" + staticPod + ".slice/cri-containerd-" + ctr + ".scope",
			CgroupIdentity{staticPod, ctr, RuntimeContainerd, QoSBurstable}},
		"pod cgroup itself": {
			"/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod" + uidEsc + ".slice",
			CgroupIdentity{PodUID: uid, QoS: QoSBurstable}},
	}
	for name, c := range cases {
		got, err := ParseCgroupPath(c.path)
		if err != nil || got != c.want {
			t.Errorf("%s: %+v, %v; want %+v", name, got, err, c.want)
		}
	}
}

// TestParseCgroupPathNotInPod: paths that name no pod say so with
// ErrNotInPod — including the "/" every process shows inside a private
// cgroup namespace — rather than guessing.
func TestParseCgroupPathNotInPod(t *testing.T) {
	for _, p := range []string{
		"/",
		"/user.slice/user-1000.slice/session-2.scope",
		"/system.slice/containerd.service",
		"/system.slice/docker-" + ctr + ".scope",
		"/docker/" + ctr,
		"/pod" + uid + "/" + ctr,                 // a pod dir outside kubepods
		"/other/burstable/pod" + uid + "/" + ctr, // the same under a foreign root
		"/machine.slice/libpod-pod" + uidEsc + ".slice",
	} {
		if id, err := ParseCgroupPath(p); !errors.Is(err, ErrNotInPod) {
			t.Errorf("%q: %+v, %v; want ErrNotInPod", p, id, err)
		}
	}
}

// TestParseCgroupPathMalformed: a kubepods hierarchy with ids the kubelet
// and runtimes never write is an error, neither a pod nor "no pod".
func TestParseCgroupPathMalformed(t *testing.T) {
	podSlice := "/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod" + uidEsc + ".slice/"
	for name, p := range map[string]string{
		"short uid":          "/kubepods.slice/kubepods-pod3b1c9a52.slice/cri-containerd-" + ctr + ".scope",
		"upper-case uid":     "/kubepods.slice/kubepods-pod" + strings.ToUpper(uidEsc) + ".slice",
		"cgroupfs escaped":   "/kubepods/burstable/pod" + uidEsc + "/" + ctr,
		"uid with traversal": "/kubepods/burstable/pod../" + ctr,
		"short container":    podSlice + "cri-containerd-4f6c0f1e.scope",
		"non-hex container":  podSlice + "cri-containerd-" + strings.Repeat("g", 64) + ".scope",
		"unknown runtime":    podSlice + "runc-" + ctr + ".scope",
		"slice not scope":    podSlice + "cri-containerd-" + ctr + ".slice",
		"crio conmon":        podSlice + "crio-conmon-" + ctr + ".scope",
		"cgroupfs conmon":    "/kubepods/pod" + uid + "/crio-conmon-" + ctr,
		"cgroupfs container": "/kubepods/pod" + uid + "/cri-containerd-" + ctr,
	} {
		id, err := ParseCgroupPath(p)
		if err == nil || errors.Is(err, ErrNotInPod) {
			t.Errorf("%s: %+v, %v; want a malformed-cgroup error", name, id, err)
		}
	}
}

// TestParseProcCgroup reads whole /proc/<pid>/cgroup files: v2 (one
// line), v1 (a line per controller) and hybrid (v1 pods, empty v2).
func TestParseProcCgroup(t *testing.T) {
	sysPath := "/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod" + uidEsc + ".slice/cri-containerd-" + ctr + ".scope"
	fsPath := "/kubepods/burstable/pod" + uid + "/" + ctr
	want := CgroupIdentity{uid, ctr, RuntimeContainerd, QoSBurstable}
	wantFS := CgroupIdentity{uid, ctr, RuntimeUnknown, QoSBurstable}
	cases := map[string]struct {
		data string
		want CgroupIdentity
	}{
		"v2": {"0::" + sysPath + "\n", want},
		"v1": {"12:pids:" + fsPath + "\n11:memory:" + fsPath + "\n" +
			"5:cpu,cpuacct:" + fsPath + "\n1:name=systemd:" + fsPath + "\n", wantFS},
		"hybrid": {"12:memory:" + sysPath + "\n1:name=systemd:" + sysPath + "\n0::/\n", want},
		// A v1 controller the kubelet does not manage (net_cls here)
		// stays at the root and is skipped.
		"v1 with root controller": {"10:net_cls,net_prio:/\n4:memory:" + fsPath + "\n", wantFS},
	}
	for name, c := range cases {
		got, err := ParseProcCgroup([]byte(c.data))
		if err != nil || got != c.want {
			t.Errorf("%s: %+v, %v; want %+v", name, got, err, c.want)
		}
	}
}

// TestParseProcCgroupNegative: a private cgroup namespace and a host
// process give ErrNotInPod; text that is not the kernel's format, and v1
// controllers naming different pods, are errors.
func TestParseProcCgroupNegative(t *testing.T) {
	other := "/kubepods/burstable/pod" + strings.Replace(uid, "3b1c", "aaaa", 1) + "/" + ctr
	fsPath := "/kubepods/burstable/pod" + uid + "/" + ctr
	for name, data := range map[string]string{
		"private namespace": "0::/\n",
		"host v1":           "4:memory:/user.slice\n1:name=systemd:/user.slice/user-1000.slice/session-2.scope\n",
	} {
		if id, err := ParseProcCgroup([]byte(data)); !errors.Is(err, ErrNotInPod) {
			t.Errorf("%s: %+v, %v; want ErrNotInPod", name, id, err)
		}
	}
	for name, data := range map[string]string{
		"empty":                "",
		"no colons":            "kubepods\n",
		"two fields":           "0:/kubepods\n",
		"bad hierarchy":        "x::/kubepods\n",
		"relative path":        "0::kubepods/pod" + uid + "\n",
		"controllers disagree": "4:memory:" + fsPath + "\n3:pids:" + other + "\n",
		"malformed v2 pod":     "0::/kubepods/pod" + uidEsc + "/" + ctr + "\n",
	} {
		id, err := ParseProcCgroup([]byte(data))
		if err == nil || errors.Is(err, ErrNotInPod) {
			t.Errorf("%s: %+v, %v; want a malformed-cgroup error", name, id, err)
		}
	}
}

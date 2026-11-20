package k8s

import (
	"errors"
	"testing"
)

// TestParseCgroupPathForged: pod-looking names a user or process could
// create where the kubelet never puts them are no pod — not trusted
// because of their spelling.
func TestParseCgroupPathForged(t *testing.T) {
	scope := "/cri-containerd-" + ctr + ".scope"
	notInPod := map[string]string{
		"user instance": "/user.slice/user-1000.slice/user@1000.service/kubepods.slice/kubepods-pod" +
			uidEsc + ".slice" + scope,
		"user slice":      "/user.slice/kubepods.slice/kubepods-pod" + uidEsc + ".slice" + scope,
		"cgroupfs user":   "/user.slice/user-1000.slice/user@1000.service/kubepods/pod" + uid + "/" + ctr,
		"inexact prefix":  "/xkubepods.slice/xkubepods-pod" + uidEsc + ".slice" + scope,
		"unknown class":   "/kubepods.slice/kubepods-guaranteed.slice/kubepods-guaranteed-pod" + uidEsc + ".slice" + scope,
		"class as suffix": "/kubepods.slice/kubepods-burstablex.slice/kubepods-burstablex-pod" + uidEsc + ".slice" + scope,
	}
	for name, p := range notInPod {
		if id, err := ParseCgroupPath(p); !errors.Is(err, ErrNotInPod) {
			t.Errorf("%s: %+v, %v; want ErrNotInPod", name, id, err)
		}
	}
	forged := map[string]string{
		"no parents":     "/kubepods-burstable-pod" + uidEsc + ".slice" + scope,
		"class missing":  "/kubepods.slice/kubepods-burstable-pod" + uidEsc + ".slice" + scope,
		"wrong parent":   "/system.slice/kubepods-pod" + uidEsc + ".slice" + scope,
		"foreign root":   "/system.slice/evil-kubepods-pod" + uidEsc + ".slice" + scope,
		"root misplaced": "/kubelet-kubepods.slice/kubelet-kubepods-pod" + uidEsc + ".slice" + scope,
	}
	for name, p := range forged {
		id, err := ParseCgroupPath(p)
		if err == nil || errors.Is(err, ErrNotInPod) {
			t.Errorf("%s: %+v, %v; want a malformed-cgroup error", name, id, err)
		}
	}
}

// TestParseCgroupPathRelative: inside the tracer's private cgroup
// namespace a sibling container shows as "/../<container>"; the
// container is extracted, the pod left to the downward API.
func TestParseCgroupPathRelative(t *testing.T) {
	ok := map[string]CgroupIdentity{
		"/../cri-containerd-" + ctr + ".scope": {ContainerID: ctr, Runtime: RuntimeContainerd},
		"/../crio-" + ctr + ".scope/container": {ContainerID: ctr, Runtime: RuntimeCRIO},
		"/../" + ctr:                           {ContainerID: ctr, Runtime: RuntimeUnknown},
	}
	for p, want := range ok {
		if got, err := ParseCgroupPath(p); err != nil || got != want {
			t.Errorf("%q: %+v, %v; want %+v", p, got, err, want)
		}
	}
	for _, p := range []string{"/../../kubepods-pod" + uidEsc + ".slice/cri-containerd-" + ctr + ".scope", "/../"} {
		if id, err := ParseCgroupPath(p); !errors.Is(err, ErrNotInPod) {
			t.Errorf("%q: %+v, %v; want ErrNotInPod", p, id, err)
		}
	}
	for _, p := range []string{"/../runc-" + ctr + ".scope", "/../crio-conmon-" + ctr + ".scope", "/../abc"} {
		if id, err := ParseCgroupPath(p); err == nil || errors.Is(err, ErrNotInPod) {
			t.Errorf("%q: %+v, %v; want a malformed-cgroup error", p, id, err)
		}
	}
	if id, err := ParseProcCgroup([]byte("0::/../cri-containerd-" + ctr + ".scope\n")); err != nil || id.ContainerID != ctr {
		t.Errorf("proc file: %+v, %v", id, err)
	}
}

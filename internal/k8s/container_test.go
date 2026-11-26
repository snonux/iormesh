package k8s

import (
	"errors"
	"strings"
	"testing"
)

// Captured on k3s on el9 (task 6k4, 2026-11-26: Rocky 9.9, kernel
// 5.14.0-741.22.1.el9_9, SELinux enforcing, k3s v1.35.10+k3s1 with its
// embedded containerd 2.2.10 under the systemd cgroup driver, cgroup v2)
// during TestKindAcceptance in namespace iormesh-m7, by a sampler on the
// node reading /proc/<pid>/cgroup as the node sees it and, through
// `nsenter -C -t <tracer>`, as the in-pod tracer's private cgroup
// namespace sees it. Two pods:
//
//   - k3sSide*: inventory-6989fddbf6-jqs2w, a burstable pod with the
//     sidecar tracer (shareProcessNamespace): the tracer container
//     "iormesh", the app container "app" (its init `sleep` and the JVM
//     both read the app's scope) and the pod sandbox's pause process;
//   - k3sDbg*: gateway-66bb954b4f-nznfc, a best-effort pod with the
//     ephemeral debug tracer "iormesh" (`kubectl debug --target app`).
//
// Every process sits in a container scope of its own: k3s on el9 put none
// in the pod slice itself, and the pause process reads as the sandbox's
// own cri-containerd-<sandbox id>.scope, so ContainerOf names the sandbox
// id for it (a sidecar tracer sees pause as pid 1). These replace task
// sh4's constructed k3s shapes; kind's captures are
// TestParseProcCgroupKindCaptures'.
const (
	k3sSideUID     = "56795aaf-b609-464e-8f6c-292450aaea2d"
	k3sSideTracer  = "e54d9ee01313dd9f59c1a40dfe98f18b4dab16866047d438d44a330f602d7e43"
	k3sSideApp     = "feb2f54761daf781477ab7ee3d3d5fedd25c99c90f1c2ec4b335fc287307754d"
	k3sSideSandbox = "b0ce75927580ac0afa804190c107b2a2f8082929b0f77bc6c88bfc5f583be8d4"
	k3sSidePodPath = "/kubepods.slice/kubepods-burstable.slice/" +
		"kubepods-burstable-pod56795aaf_b609_464e_8f6c_292450aaea2d.slice/"
	k3sSideTracerInNode  = "0::" + k3sSidePodPath + "cri-containerd-" + k3sSideTracer + ".scope\n"
	k3sSideAppInNode     = "0::" + k3sSidePodPath + "cri-containerd-" + k3sSideApp + ".scope\n"
	k3sSidePauseInNode   = "0::" + k3sSidePodPath + "cri-containerd-" + k3sSideSandbox + ".scope\n"
	k3sSideTracerOwnView = "0::/\n"
	k3sSideAppFromTracer = "0::/../cri-containerd-" + k3sSideApp + ".scope\n"
	k3sSidePauseFromTrcr = "0::/../cri-containerd-" + k3sSideSandbox + ".scope\n"

	k3sDbgUID     = "3c2ab314-593b-402b-af61-af5830ad8e0e"
	k3sDbgTracer  = "cd383991acd66f03d7d8d5b0a1cf88a50116202a16240c0677dc93627e2556e4"
	k3sDbgApp     = "a0ca1ded0a73baf9bfdd7a2fd5cb54d9592f9ff73be5c685c4e0669b1d967998"
	k3sDbgSandbox = "37fc8e169e0fb23d3c73a818b868f5b6d8b3a28aa652a3f8e6ca50a10309595a"
	k3sDbgPodPath = "/kubepods.slice/kubepods-besteffort.slice/" +
		"kubepods-besteffort-pod3c2ab314_593b_402b_af61_af5830ad8e0e.slice/"
	k3sDbgTracerInNode  = "0::" + k3sDbgPodPath + "cri-containerd-" + k3sDbgTracer + ".scope\n"
	k3sDbgAppInNode     = "0::" + k3sDbgPodPath + "cri-containerd-" + k3sDbgApp + ".scope\n"
	k3sDbgPauseInNode   = "0::" + k3sDbgPodPath + "cri-containerd-" + k3sDbgSandbox + ".scope\n"
	k3sDbgAppFromTracer = "0::/../cri-containerd-" + k3sDbgApp + ".scope\n"

	// The same sidecar pod in the run's nftables pass (another pod UID),
	// read through `nsenter -C -t <app's sleep>`: the app's own view and
	// its view of the tracer. The sandbox's pause shares the node's
	// cgroup namespace and reads the full path.
	k3sSide2UID       = "7693cced-2204-43d6-bc99-231f79cbd9ac"
	k3sSide2Tracer    = "352c1531193bb9dae232a08310ee3c270496aba232fed34f8f7bcef43024fe55"
	k3sSide2AppOwn    = "0::/\n"
	k3sSide2TracerApp = "0::/../cri-containerd-" + k3sSide2Tracer + ".scope\n"
)

// TestContainerOfShapes: ContainerOf names the container of a process of
// the tracer's pod in every shape a tracer meets — kind's and k3s on
// el9's captures, full or relative to the tracer's own cgroup namespace,
// and the cgroupfs driver's paths (not captured: k3s on el9 runs the
// systemd driver; built from TestParseCgroupPathRuntimes' ids) — and
// names none for the tracer's own container ("0::/"), the pod cgroup
// itself, another pod's container, a host process, or a file that is not
// the kernel's. The pod sandbox's pause process is in a container scope
// of its own, so it names the sandbox's id.
func TestContainerOfShapes(t *testing.T) {
	for name, c := range map[string]struct {
		data, pod, want string
	}{
		"kind app in node":            {kindInNode, kindPodUID, kindAppCtr},
		"kind app from host":          {kindFromHost, kindPodUID, kindAppCtr},
		"kind tracer from app":        {kindDbgFromApp, kindPodUID, kindDbgCtr},
		"k3s sidecar app in node":     {k3sSideAppInNode, k3sSideUID, k3sSideApp},
		"k3s sidecar app from tracer": {k3sSideAppFromTracer, k3sSideUID, k3sSideApp},
		"k3s sidecar tracer in node":  {k3sSideTracerInNode, k3sSideUID, k3sSideTracer},
		"k3s pause in node":           {k3sSidePauseInNode, k3sSideUID, k3sSideSandbox},
		"k3s pause from tracer":       {k3sSidePauseFromTrcr, k3sSideUID, k3sSideSandbox},
		"k3s debug app in node":       {k3sDbgAppInNode, k3sDbgUID, k3sDbgApp},
		"k3s debug app from tracer":   {k3sDbgAppFromTracer, k3sDbgUID, k3sDbgApp},
		"k3s tracer from app":         {k3sSide2TracerApp, k3sSide2UID, k3sSide2Tracer},
		"k3s app's own view":          {k3sSide2AppOwn, k3sSide2UID, ""},
		"cgroupfs":                    {"0::/kubepods/burstable/pod" + uid + "/" + ctr + "\n", uid, ctr},
		"cgroupfs relative":           {"0::/../" + ctr + "\n", uid, ctr},
		"own container":               {k3sSideTracerOwnView, k3sSideUID, ""},
		"pod cgroup itself":           {"0::" + strings.TrimSuffix(k3sSidePodPath, "/") + "\n", k3sSideUID, ""},
		"another pod":                 {k3sDbgAppInNode, k3sSideUID, ""},
		"another pod (kind)":          {k3sSideAppInNode, kindPodUID, ""},
		"host process":                {"0::/system.slice/sshd.service\n", k3sSideUID, ""},
		"climbs out of the pod":       {"0::/../../kubepods-besteffort.slice\n", k3sDbgUID, ""},
		"not the kernel's":            {"garbage\n", k3sSideUID, ""},
		"empty (process is gone)":     {"", k3sSideUID, ""},
	} {
		if got := ContainerOf([]byte(c.data), c.pod); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

// TestParseProcCgroupK3sCaptures: the files captured on k3s on el9 parse
// to each pod's UID, QoS class and container — the tracer, the app and
// the sandbox's pause alike — the tracer's own view is ErrNotInPod, and a
// sibling seen from inside the tracer's cgroup namespace yields its
// container id alone.
func TestParseProcCgroupK3sCaptures(t *testing.T) {
	side := func(ctr string) CgroupIdentity {
		return CgroupIdentity{k3sSideUID, ctr, RuntimeContainerd, QoSBurstable}
	}
	dbg := func(ctr string) CgroupIdentity {
		return CgroupIdentity{k3sDbgUID, ctr, RuntimeContainerd, QoSBestEffort}
	}
	rel := func(ctr string) CgroupIdentity {
		return CgroupIdentity{ContainerID: ctr, Runtime: RuntimeContainerd}
	}
	for name, c := range map[string]struct {
		data string
		want CgroupIdentity
	}{
		"sidecar tracer in node":  {k3sSideTracerInNode, side(k3sSideTracer)},
		"sidecar app in node":     {k3sSideAppInNode, side(k3sSideApp)},
		"sidecar pause in node":   {k3sSidePauseInNode, side(k3sSideSandbox)},
		"sidecar app from tracer": {k3sSideAppFromTracer, rel(k3sSideApp)},
		"pause from tracer":       {k3sSidePauseFromTrcr, rel(k3sSideSandbox)},
		"debug tracer in node":    {k3sDbgTracerInNode, dbg(k3sDbgTracer)},
		"debug app in node":       {k3sDbgAppInNode, dbg(k3sDbgApp)},
		"debug pause in node":     {k3sDbgPauseInNode, dbg(k3sDbgSandbox)},
		"debug app from tracer":   {k3sDbgAppFromTracer, rel(k3sDbgApp)},
		"tracer from app":         {k3sSide2TracerApp, rel(k3sSide2Tracer)},
	} {
		got, err := ParseProcCgroup([]byte(c.data))
		if err != nil || got != c.want {
			t.Errorf("%s: %+v, %v; want %+v", name, got, err, c.want)
		}
	}
	for name, data := range map[string]string{
		"tracer's own view": k3sSideTracerOwnView,
		"app's own view":    k3sSide2AppOwn,
	} {
		if id, err := ParseProcCgroup([]byte(data)); !errors.Is(err, ErrNotInPod) {
			t.Errorf("%s: %+v, %v; want ErrNotInPod", name, id, err)
		}
	}
}

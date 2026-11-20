package k8s

import (
	"fmt"
	"strconv"
)

// The pod-level subset of the Kubernetes API types the patches use, with
// the API's field names, so a strategic-merge patch of a Deployment or
// StatefulSet (`kubectl patch --patch-file`) decodes as the API server
// would read it. Field order is the rendering order. The tracer
// container itself is customSpec's (debug.go), the one spec the debug
// container and the sidecar share.

type workloadPatch struct {
	Spec struct {
		Template struct {
			Spec podSpec `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

type podSpec struct {
	HostUsers             *bool            `yaml:"hostUsers,omitempty"`
	ShareProcessNamespace *bool            `yaml:"shareProcessNamespace,omitempty"`
	Containers            []map[string]any `yaml:"containers,omitempty"`
	Volumes               []volume         `yaml:"volumes"`
}

type volume struct {
	Name     string    `yaml:"name"`
	HostPath *hostPath `yaml:"hostPath,omitempty"`
	EmptyDir *emptyDir `yaml:"emptyDir,omitempty"`
}

type hostPath struct {
	Path string `yaml:"path"`
	Type string `yaml:"type"`
}

type emptyDir struct {
	SizeLimit string `yaml:"sizeLimit"`
}

// preparePatch is the pod-template patch every mode needs (decision
// #13): tracefs read-only from the host (libbpf reads tracepoint ids
// there; mounting tracefs itself needs SYS_ADMIN) and the bounded trace
// dir, plus hostUsers: true — a user-namespaced pod's capabilities do
// not reach BPF or the host kernel's tracing. Kubectl.Check refuses a
// pod without the two volumes or with hostUsers: false; it accepts the
// field unset, the API's default being host users too.
func (m Manifests) preparePatch() workloadPatch {
	var p workloadPatch
	hostUsers := true
	p.Spec.Template.Spec = podSpec{HostUsers: &hostUsers, Volumes: []volume{
		// Directory: the kubelet refuses to start the pod only if the
		// node has no /sys/kernel/tracing at all. It does not prove
		// tracefs is mounted there (sysfs keeps the empty mount point
		// either way); a node without it shows as the tracer attaching
		// nothing (errNothingAttached).
		{Name: TracefsVolume, HostPath: &hostPath{Path: TracefsPath, Type: "Directory"}},
		{Name: TracesVolume, EmptyDir: &emptyDir{SizeLimit: m.TraceLimit}},
	}}
	return p
}

// sidecarPatch is the prepare patch plus the tracer container and one PID
// namespace for the pod (decision #14: the Java attach goes through
// /proc/<pid>/root, and the tracer keys its targets by the pod's pids).
// The container is the debug container's spec (customSpec, minimal
// profile) with what kubectl debug's flags would otherwise set. It is a
// regular container, not a native sidecar (an init container with
// restartPolicy: Always); docs/implementation.md M7 step 8 records the
// trade-off for task yb4.
func (m Manifests) sidecarPatch() workloadPatch {
	p := m.preparePatch()
	share := true
	p.Spec.Template.Spec.ShareProcessNamespace = &share
	c := customSpec(ProfileMinimal)
	c["name"], c["image"], c["args"] = TracerContainer, m.Image, m.sidecarArgs()
	// The container is the unit (PLAN.md M7): its limits are the caps
	// `iormesh start` puts on a server's unit by default (one CPU, 1 GiB).
	// Small explicit requests keep the scheduler from reserving the
	// limits (requests default to limits when unset) for a tracer that
	// mostly waits for events.
	c["resources"] = map[string]any{
		"requests": map[string]string{"cpu": "50m", "memory": "128Mi"},
		"limits":   map[string]string{"cpu": "1", "memory": "1Gi"},
	}
	p.Spec.Template.Spec.Containers = []map[string]any{c}
	return p
}

// sidecarArgs are SidecarArgs plus the trace dir's byte cap
// (-disk-max-bytes, TraceCap), so the tracer ends its trace before the
// kubelet evicts the pod — the app with it — for outgrowing the
// emptyDir's sizeLimit.
func (m Manifests) sidecarArgs() []string {
	return append(append([]string(nil), m.SidecarArgs...), "-disk-max-bytes", TraceCap(m.TraceLimit))
}

// TraceCap is the tracer's -disk-max-bytes for an emptyDir of sizeLimit
// limit (a binaryQuantity): three quarters of it, in MiB. The quarter
// left covers the segment being written when the cap trips (the cap is
// checked every few seconds and at each rotation, segments are at most
// 64 MB) and the session's end files. "" for a limit that is not a
// binaryQuantity. The controller passes the same for a debug container,
// from the pod's own sizeLimit.
func TraceCap(limit string) string {
	if !binaryQuantity.MatchString(limit) {
		return ""
	}
	num, unit := limit[:len(limit)-2], limit[len(limit)-2:]
	n, err := strconv.ParseUint(num, 10, 64)
	if err != nil {
		return ""
	}
	capMiB := n * map[string]uint64{"Mi": 1, "Gi": 1 << 10, "Ti": 1 << 20}[unit] * 3 / 4
	if capMiB == 0 {
		return "" // 0 would turn the cap off
	}
	return fmt.Sprintf("%dMiB", capMiB)
}

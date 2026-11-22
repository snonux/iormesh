package k8s

import (
	"fmt"
	"slices"
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
	c := customSpec(ProfileMinimal, DefaultSELinuxType, DefaultSELinuxLevel)
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

// sidecarArgs are SidecarArgs plus the pod's disk bounds (PodDiskArgs).
// They come last, so they win over the same flags in SidecarArgs (Go's
// flag package keeps a repeated flag's last value).
func (m Manifests) sidecarArgs() []string {
	return slices.Concat(m.SidecarArgs, PodDiskArgs(m.TraceLimit))
}

// PodDiskArgs are an in-pod tracer's disk bounds for a trace emptyDir of
// sizeLimit limit (task df4), which the sidecar patch and a debug deploy
// both pass:
//
//   - -disk-max-bytes TraceCap(limit): the trace dir's own byte cap, so
//     the tracer ends its trace before the kubelet evicts the pod — the
//     app with it — for outgrowing the emptyDir's sizeLimit;
//   - -disk-limit 0: no percent-used limit. statfs on a disk-backed
//     emptyDir sees the node's partition, not the volume, so the
//     default 90 % stopped every tracer on a node at once whenever the
//     node's disk was that full for reasons of its own (seen on kind,
//     task yb4: a 96 %-full dev disk). The node's fullness is the
//     kubelet's to manage (its eviction thresholds), the volume's is the
//     byte cap's.
//
// -disk-min-free keeps its default (256 MiB free on the partition): a
// tracer still never writes a node's last bytes.
//
// At either bound a run in a pod ends its trace, frees its BPF and then
// waits — it does not exit — until collect has drained the trace dir
// back under them or SIGTERM arrives (task ki4, internal/cli
// awaitDrain): an exit would have the kubelet restart the sidecar into
// a run refused for the same full dir, a crash loop that takes the pod
// NotReady and leaves collect no running container to exec into.
func PodDiskArgs(limit string) []string {
	return []string{"-disk-limit", "0", "-disk-max-bytes", TraceCap(limit)}
}

// The headroom a trace emptyDir keeps above the tracer's byte cap
// (task 5g4). The cap is checked every few seconds and before each
// rotation, and a segment is written out only when it rotates: when
// the cap trips, the dir may still grow by the segment being built (at
// most segmentBytes, decision #16, plus one tick's events) and by the
// session's end files. A quarter of the volume covers that for a large
// sizeLimit; for a small one it does not (a quarter of 200Mi is 50Mi,
// less than one segment), so the cap is the smaller of three quarters
// and the volume less traceReserve, and a sizeLimit whose cap would
// fall under minTraceCap is refused — it holds too little trace to be
// worth starting a tracer, and less headroom risks the pod's eviction.
const (
	segmentBytes = 64 << 20              // internal/segment.DefaultMaxBytes
	traceReserve = segmentBytes + 32<<20 // a segment, a tick's events, the end files
	minTraceCap  = 32 << 20              // so the smallest sizeLimit is 128Mi
	mib          = 1 << 20
)

// TraceCap is the tracer's -disk-max-bytes for an emptyDir of sizeLimit
// limit, in MiB: min(¾ limit, limit − traceReserve), rounded down to a
// MiB (see traceReserve for why). limit is any byte-size Quantity
// (parseQuantity: 2Gi, 2G, 2000M, 2147483648, 2e9). "" for a limit
// that is no size, or too small to leave minTraceCap — the caller
// refuses it rather than start a tracer whose cap could let the
// kubelet evict the pod. A debug deploy passes the same, from the pod's
// own sizeLimit (Kubectl.podDiskArgs).
func TraceCap(limit string) string {
	n, err := parseQuantity(limit)
	if err != nil || n < minTraceCap+traceReserve {
		return ""
	}
	capBytes := min(n/4*3, n-traceReserve)
	return fmt.Sprintf("%dMiB", capBytes/mib)
}

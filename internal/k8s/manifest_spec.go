package k8s

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
// pod without exactly these.
func (m Manifests) preparePatch() workloadPatch {
	var p workloadPatch
	hostUsers := true
	p.Spec.Template.Spec = podSpec{HostUsers: &hostUsers, Volumes: []volume{
		// Directory: a node without tracefs there fails the pod's start,
		// rather than the tracer later attaching nothing.
		{Name: TracefsVolume, HostPath: &hostPath{Path: TracefsPath, Type: "Directory"}},
		{Name: TracesVolume, EmptyDir: &emptyDir{SizeLimit: m.TraceLimit}},
	}}
	return p
}

// sidecarPatch is the prepare patch plus the tracer container and one PID
// namespace for the pod (decision #14: the Java attach goes through
// /proc/<pid>/root, and the tracer keys its targets by the pod's pids).
// The container is the debug container's spec (customSpec, minimal
// profile) with what kubectl debug's flags would otherwise set.
func (m Manifests) sidecarPatch() workloadPatch {
	p := m.preparePatch()
	share := true
	p.Spec.Template.Spec.ShareProcessNamespace = &share
	c := customSpec(ProfileMinimal)
	c["name"], c["image"], c["args"] = TracerContainer, m.Image, m.SidecarArgs
	// The container is the unit (PLAN.md M7): its limits are the caps
	// `iormesh start` puts on a server's unit by default (one CPU, 1 GiB).
	c["resources"] = map[string]any{"limits": map[string]string{"cpu": "1", "memory": "1Gi"}}
	p.Spec.Template.Spec.Containers = []map[string]any{c}
	return p
}

package k8s

import (
	"fmt"

	"iormesh/internal/project"
)

// The downward-API variables the tracer container carries (manifest.go
// sets them from the pod's metadata). Under a private cgroup namespace,
// cgroup v2's default, a tracer's own /proc/self/cgroup reads "0::/", so
// these are how an in-pod tracer knows its pod (docs/implementation.md
// M7 step 6).
const (
	EnvPodName      = "POD_NAME"
	EnvPodNamespace = "POD_NAMESPACE"
	EnvPodUID       = "POD_UID"
)

// downwardEnv is the tracer container's env: each variable from the pod's
// own metadata (fieldRef), never a literal a patch author could get wrong.
func downwardEnv() []map[string]any {
	field := func(name, path string) map[string]any {
		return map[string]any{"name": name, "valueFrom": map[string]any{
			"fieldRef": map[string]any{"fieldPath": path}}}
	}
	return []map[string]any{field(EnvPodName, "metadata.name"),
		field(EnvPodNamespace, "metadata.namespace"), field(EnvPodUID, "metadata.uid")}
}

// PodRef is the pod a tracer runs in, as the downward API names it.
type PodRef struct {
	Name, Namespace, UID string
}

// PodFromEnv reads the downward-API variables through getenv (os.Getenv
// in production). ok is false unless all three are set: a host process
// that happens to carry one of them is not in a pod, and a partial
// identity would mislabel the trace.
func PodFromEnv(getenv func(string) string) (PodRef, bool) {
	p := PodRef{Name: getenv(EnvPodName), Namespace: getenv(EnvPodNamespace), UID: getenv(EnvPodUID)}
	return p, p.Name != "" && p.Namespace != "" && p.UID != ""
}

// ProjectForNamespace is the project label a tracer in namespace ns uses
// when none is given (decision #21: in k8s mode the project defaults to
// the namespace). Every valid namespace name is a valid project label,
// but the namespace arrives through the environment, so both rules are
// checked rather than assumed.
func ProjectForNamespace(ns string) (string, error) {
	// labelName (target.go): a namespace name is an RFC 1123 label.
	if !labelName.MatchString(ns) || !project.Valid(ns) {
		return "", fmt.Errorf("namespace %q: not a Kubernetes namespace name usable as a project (%s)",
			ns, project.Rule)
	}
	return ns, nil
}

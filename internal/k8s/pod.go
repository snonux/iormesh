package k8s

import (
	"errors"
	"fmt"

	"iormesh/internal/k8sname"
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

// ErrPodEnv marks a downward-API environment the tracer refuses: one or
// two of the three variables set, or a value that is not what the API
// server issues.
var ErrPodEnv = errors.New("k8s: pod environment refused")

// PodFromEnv reads the downward-API variables through getenv (os.Getenv
// in production). None set: not in a pod (ok false, no error). All three
// set and well-formed: in a pod. Anything else is ErrPodEnv, never a
// silent fallback to the host's identity: in a pod the tracer's host id
// and FQDN come from these values (HostID, FQDN), the image has no
// /etc/machine-id to fall back to, and a container's machine-id would
// not tell two pods apart anyway. A partial or malformed set is a broken
// patch or a forged environment; either way the trace would carry an
// identity that is not the pod's, and a refused start is what an
// operator sees and fixes. The checks are strict because the values
// reach segment file names, the footer and every event's host_id.
func PodFromEnv(getenv func(string) string) (PodRef, bool, error) {
	p := PodRef{Name: getenv(EnvPodName), Namespace: getenv(EnvPodNamespace), UID: getenv(EnvPodUID)}
	if p == (PodRef{}) {
		return p, false, nil
	}
	if err := p.Validate(); err != nil {
		return PodRef{}, false, fmt.Errorf("%w; set %s, %s and %s from the pod's downward API, "+
			"or unset all three for a run outside a pod", err, EnvPodName, EnvPodNamespace, EnvPodUID)
	}
	return p, true, nil
}

// MaxFQDN bounds a pod's FQDN (Name.Namespace.<8 hex>): a segment file
// name is <fqdn>-<session 8>-<start 16>-<seq 6+>.<table>.parquet, at most
// 249 bytes (ready.go's segmentName: the controller's unpack temporary
// "." + name + ".part" must fit NAME_MAX, 255), and the rest takes 49 of
// them with a six-digit seq; 190 leaves room for a seq of up to 16
// digits. A pod name long enough to exceed it (pod names may have 253
// bytes) is refused, not cut: a truncated name could collide with
// another pod's.
const MaxFQDN = 190

// Validate checks p strictly, by the very rules the controller applies to
// the same pod's API metadata before it pins the pod's identity (Lister,
// k8sname): the UID as the API server issues it, the name an RFC 1123
// subdomain, the namespace an RFC 1123 label, and the FQDN they make
// within MaxFQDN. A static pod is refused: its downward-API UID is the
// kubelet's 32-hex hash, not the mirror pod's metadata.uid the
// controller would pin, so the two sides would disagree on its identity
// (and neither a debug container nor a sidecar patch reaches a static
// pod through the API anyway). The UID check also makes the identity
// lower case and its 8-hex FQDN suffix exact, as the controller's is.
func (p PodRef) Validate() error {
	switch {
	case p.Name == "" || p.Namespace == "" || p.UID == "":
		return fmt.Errorf("%w: %s, %s and %s must be set all or none (have %q, %q, %q)", ErrPodEnv,
			EnvPodName, EnvPodNamespace, EnvPodUID, p.Name, p.Namespace, p.UID)
	case !k8sname.UID(p.UID):
		return fmt.Errorf("%w: %s %q is not a pod UID as the API server issues it", ErrPodEnv, EnvPodUID, p.UID)
	case !k8sname.Pod(p.Name):
		return fmt.Errorf("%w: %s %q is not an RFC 1123 subdomain", ErrPodEnv, EnvPodName, p.Name)
	case !k8sname.Label(p.Namespace):
		return fmt.Errorf("%w: %s %q is not an RFC 1123 label", ErrPodEnv, EnvPodNamespace, p.Namespace)
	case len(p.FQDN()) > MaxFQDN:
		return fmt.Errorf("%w: pod FQDN %q is longer than %d bytes", ErrPodEnv, p.FQDN(), MaxFQDN)
	}
	return nil
}

// HostID is the host_id a tracer in pod p stamps on every event,
// PodHostID: "pod-" and the pod UID (PLAN.md M7, task 7f4) — the same
// helper the controller pins the pod's store directory with, so the two
// can never disagree. In k8s mode the graph's host node is the pod. Its
// tracer sees the pod's PID namespace, so pids are unique only within
// the pod; keying by the pod keeps proc:<host_id>/<pid>@<inc> of two
// pods on one node apart, given one tracer per pod (PLAN.md M7). The
// pods of one node share the session's boot_id, which groups them by
// node.
func (p PodRef) HostID() string { return PodHostID(p.UID) }

// FQDN is the name a tracer in pod p gives its host, PodFQDN (the
// controller's helper too): Name.Namespace and the UID's first 8 hex
// digits, which tell apart a pod recreated under the same name (a
// StatefulSet's). No resolver is asked: a pod's DNS names depend on the
// cluster and its Services, and the trace start must not wait on them.
func (p PodRef) FQDN() string { return PodFQDN(p.Name, p.Namespace, p.UID) }

// ProjectForNamespace is the project label a tracer in namespace ns uses
// when none is given (decision #21: in k8s mode the project defaults to
// the namespace). Every valid namespace name is a valid project label,
// but the namespace arrives through the environment, so both rules are
// checked rather than assumed.
func ProjectForNamespace(ns string) (string, error) {
	// k8sname.Label: a namespace name is an RFC 1123 label.
	if !k8sname.Label(ns) || !project.Valid(ns) {
		return "", fmt.Errorf("namespace %q: not a Kubernetes namespace name usable as a project (%s)",
			ns, project.Rule)
	}
	return ns, nil
}

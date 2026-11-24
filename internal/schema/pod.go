package schema

import "strings"

// podHostPrefix starts an in-pod tracer's host_id, "pod-<uid>"
// (k8sname.PodHostPrefix; schema imports no internal package).
const podHostPrefix = "pod-"

// containerIDLen is a runtime container id's length: containerd, CRI-O
// and Docker all use 64 hex digits.
const containerIDLen = 64

// validatePod keeps the pod object to what an in-pod tracer writes
// (docs/milestones.md M7 "Pod fields on events", task sh4): only on a pod's
// host id, names the API server could have issued — they reach the
// controller's namespace filter and the operator's terminal — and a container
// id only on an event of a process. The checks are byte loops, not k8sname's
// regexps: the sink validates every event (decision #25's budget), and
// TestPodNameRulesMatchK8sname holds the two to the same rules.
func (e *Event) validatePod() error {
	p := e.Pod
	if p == nil {
		return nil
	}
	switch {
	case !strings.HasPrefix(e.HostID, podHostPrefix):
		return invalidf("pod object on host %q (an in-pod tracer's host_id is pod-<uid>)", e.HostID)
	case !dnsLabel(p.Namespace):
		return invalidf("pod.namespace %q is not an RFC 1123 label", p.Namespace)
	case !dnsSubdomain(p.Name):
		return invalidf("pod.name %q is not an RFC 1123 subdomain", p.Name)
	case p.ContainerID == "":
		return nil
	case e.PID == 0:
		return invalidf("pod.container_id on a host-level %s event", e.Event)
	case !lowerHex(p.ContainerID, containerIDLen):
		return invalidf("pod.container_id %q is not %d lowercase hex digits", p.ContainerID, containerIDLen)
	}
	return nil
}

// dnsLabel is k8sname.Label: 1-63 of [a-z0-9-], alphanumeric at both
// ends.
func dnsLabel(s string) bool {
	return len(s) <= 63 && dnsPart(s)
}

// dnsSubdomain is k8sname.Pod: at most 253 bytes of dot-separated parts,
// each [a-z0-9-] and alphanumeric at both ends.
func dnsSubdomain(s string) bool {
	if len(s) > 253 {
		return false
	}
	for part := range strings.SplitSeq(s, ".") {
		if !dnsPart(part) {
			return false
		}
	}
	return true
}

// dnsPart reports a non-empty run of [a-z0-9-] that starts and ends
// alphanumeric.
func dnsPart(s string) bool {
	if s == "" || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// lowerHex reports exactly n lowercase hex digits.
func lowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := range len(s) {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

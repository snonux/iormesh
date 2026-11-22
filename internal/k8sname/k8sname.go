// Package k8sname holds the Kubernetes name rules iormesh checks before a
// value reaches a kubectl argv (PLAN.md M7, decision #18): namespaces,
// containers and cluster labels, pod names, kubeconfig contexts, images
// pinned by digest, the label selectors a registry pods: entry picks
// pods by, and the SELinux type and MCS level a minimal-profile tracer
// runs as. It is its own package because two sides need the same rules:
// internal/federate validates the registry's k8s: and pods: sections
// when it loads them, and internal/k8s validates every Target again
// before it runs kubectl — and internal/k8s imports internal/federate
// (its Transport), so the rules cannot live in either.
package k8sname

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	// label is a DNS-1123 label: namespaces, containers, and the
	// registry's cluster labels.
	label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	// contextName is what kubeconfig context names look like in practice
	// (kind-x, user@cluster, arn:aws:eks:…:cluster/x): no space, quote,
	// control character or leading dash.
	contextName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._:@/+-]{0,252}$`)
	// pinned is a repository (optionally with a registry host, port and
	// tag) followed by a sha256 digest: the digest, not a tag, says which
	// bytes run as root in the pod.
	pinned = regexp.MustCompile(
		`^[a-z0-9][a-z0-9._-]*(?::[0-9]+)?(?:/[a-z0-9][a-z0-9._-]*)*(?::[A-Za-z0-9_][A-Za-z0-9._-]{0,127})?` +
			`@sha256:([0-9a-f]{64})$`)
	// selectorTerm is one equality-based label selector requirement:
	// an optional DNS-subdomain prefix and a name, then =, == or !=,
	// then a label value (possibly empty).
	selectorTerm = regexp.MustCompile(
		`^(?:[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?/)?[A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?` +
			`(=|==|!=)([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$`)
	// repoDigest is the digest a container status's imageID names:
	// "<repo>@sha256:<hex>" (containerd, CRI-O) or the same behind
	// "docker-pullable://" (cri-dockerd).
	repoDigest = regexp.MustCompile(`@sha256:([0-9a-f]{64})$`)
	// selinuxType is an SELinux type name as the policy spells one
	// (spc_t, container_t, iormesh_container_t): lowercase letters,
	// digits and underscores, nothing that could carry a user, role or
	// level into seLinuxOptions.
	selinuxType = regexp.MustCompile(`^[a-z0-9_]{1,128}$`)
	// selinuxLevel is an MCS level as a pod's label carries one: the
	// sensitivity s0 alone or with a category pair, each category
	// c0..c1023 spelled without leading zeros (the policy's names). The
	// range check and the distinct pair are SELinuxLevel's.
	selinuxLevel = regexp.MustCompile(`^s0(?::c(0|[1-9][0-9]{0,3}),c(0|[1-9][0-9]{0,3}))?$`)
)

// maxCategory is the highest MCS category the targeted policy defines
// (c1023).
const maxCategory = 1023

// SELinuxLevel reports whether s is an MCS level for a tracer container's
// seLinuxOptions.level (decision #13, task 8h4): "s0", or "s0:cN,cM" with
// two distinct categories N, M <= 1023 — the form containerd gives a pod
// and an owner pins one to. Anything else (a range, a sensitivity above
// s0, a user or type, a lone category) is refused. The empty string,
// which omits the level, is the caller's to allow.
func SELinuxLevel(s string) bool {
	m := selinuxLevel.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	if m[1] == "" {
		return true
	}
	n, errN := strconv.Atoi(m[1])
	k, errK := strconv.Atoi(m[2])
	return errN == nil && errK == nil && n <= maxCategory && k <= maxCategory && n != k
}

// SELinuxType reports whether s is an SELinux type name, the value of a
// tracer container's seLinuxOptions.type (decision #13, task 5h4). The
// empty string, which omits the field, is the caller's to allow.
func SELinuxType(s string) bool { return selinuxType.MatchString(s) }

// Label reports whether s is a DNS-1123 label.
func Label(s string) bool { return label.MatchString(s) }

// subdomainPart is one dot-separated part of a DNS-1123 subdomain as
// Kubernetes checks it (IsDNS1123Subdomain): alphanumeric at both ends,
// dashes inside, and no per-part length cap — only the whole name's 253
// bytes. A pod named with one 64-byte part is valid and must pass.
var subdomainPart = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// Pod reports whether s is a valid pod name: a DNS-1123 subdomain, at
// most 253 bytes of dot-joined parts — strictly, so "a..b" and "a-.b",
// which the API server never issues, fail too. A pod name is also part
// of an in-pod tracer's FQDN (internal/k8s PodFQDN, task 7f4), which the
// tracer and the controller must accept by the same rule.
func Pod(s string) bool {
	if len(s) > 253 {
		return false
	}
	for l := range strings.SplitSeq(s, ".") {
		if !subdomainPart.MatchString(l) {
			return false
		}
	}
	return true
}

// Context reports whether s is a usable kubeconfig context name.
func Context(s string) bool { return contextName.MatchString(s) }

// Pinned reports whether image is pinned by a sha256 digest.
func Pinned(image string) bool { return pinned.MatchString(image) }

// Digest returns the hex sha256 digest image is pinned by, or "".
func Digest(image string) string {
	m := pinned.FindStringSubmatch(image)
	if m == nil {
		return ""
	}
	return m[1]
}

// StatusDigest returns the repository digest a container status's
// imageID names, or "" when it names none — an imageID of a bare
// "sha256:<hex>" is the image's config digest, a local image id, which no
// pull by digest verified, so it proves nothing about what the registry
// served under the pinned digest.
func StatusDigest(imageID string) string {
	m := repoDigest.FindStringSubmatch(imageID)
	if m == nil {
		return ""
	}
	return m[1]
}

// Selector reports whether s is a label selector a registry pods: entry
// may carry: equality-based requirements only (k=v, k==v, k!=v), comma
// separated, at least one of them positive (= or ==). Set-based and
// existence requirements are left out to keep the rule small, and a
// selector of only != terms is refused — it would pick nearly every pod
// of the namespace, and an empty one picks all of them.
func Selector(s string) bool {
	if s == "" || len(s) > 1024 {
		return false
	}
	positive := false
	for term := range strings.SplitSeq(s, ",") {
		m := selectorTerm.FindStringSubmatch(term)
		if m == nil {
			return false
		}
		if m[3] != "!=" {
			positive = true
		}
	}
	return positive
}

// uid is the form the API server gives every object's metadata.uid.
var uid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// UID reports whether s is an object UID as the API server writes it.
// A pod's UID names its store directory on the controller, so nothing
// else passes.
func UID(s string) bool { return uid.MatchString(s) }

// PodHostPrefix starts the host_id of an in-pod tracer, "pod-<uid>"
// (PLAN.md M7, task 7f4). It lives here, beside UID, so internal/k8s
// (which writes it: k8s.PodHostID) and internal/routemap (which reads
// it back to find a pod by UID, and cannot import internal/k8s) spell it
// once (task 5g4).
const PodHostPrefix = "pod-"

// PodHostID is the host_id of the tracer in the pod with this UID.
func PodHostID(uid string) string { return PodHostPrefix + uid }

// PodNamespace returns the namespace an in-pod tracer's host identity
// names: host_id "pod-<uid>" and FQDN "<name>.<namespace>.<uid[:8]>"
// (internal/k8s PodFQDN, task 7f4). ok is false for any other identity —
// a machine-id host, or a pod FQDN that does not end in its own UID's
// prefix. The namespace is an RFC 1123 label, so it holds no dot and is
// the FQDN's second-to-last part, whatever dots the pod name has. The
// controller's namespace filter (task sh4) reads it from the pinned
// identity, which collect derived from the pod's API metadata.
func PodNamespace(hostID, fqdn string) (string, bool) {
	uid, ok := strings.CutPrefix(hostID, PodHostPrefix)
	if !ok || !UID(uid) {
		return "", false
	}
	rest, ok := strings.CutSuffix(fqdn, "."+uid[:8])
	i := strings.LastIndexByte(rest, '.')
	if !ok || i <= 0 || !Pod(rest[:i]) || !Label(rest[i+1:]) {
		return "", false
	}
	return rest[i+1:], true
}

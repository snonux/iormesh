// Package k8sname holds the Kubernetes name rules iormesh checks before a
// value reaches a kubectl argv (PLAN.md M7, decision #18): namespaces,
// containers and cluster labels, pod names, kubeconfig contexts, images
// pinned by digest and the label selectors a registry pods: entry picks
// pods by. It is its own package because two sides need the same rules:
// internal/federate validates the registry's k8s: and pods: sections
// when it loads them, and internal/k8s validates every Target again
// before it runs kubectl — and internal/k8s imports internal/federate
// (its Transport), so the rules cannot live in either.
package k8sname

import (
	"regexp"
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
)

// Label reports whether s is a DNS-1123 label.
func Label(s string) bool { return label.MatchString(s) }

// Pod reports whether s is a valid pod name: a DNS-1123 subdomain, at
// most 253 bytes of dot-joined labels — strictly, so "a..b" and "a-.b",
// which the API server never issues, fail too. A pod name is also part
// of an in-pod tracer's FQDN (internal/k8s PodFQDN, task 7f4), which the
// tracer and the controller must accept by the same rule.
func Pod(s string) bool {
	if len(s) > 253 {
		return false
	}
	for l := range strings.SplitSeq(s, ".") {
		if !label.MatchString(l) {
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

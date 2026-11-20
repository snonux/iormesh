// Package k8s is the controller's Kubernetes side (PLAN.md M7, decisions
// #12, #13, #14 and #18; docs/implementation.md M7 steps 5 and 9): the
// Kubectl transport, which reaches a tracer container in a pod through
// the operator's kubectl the way federate.SSH reaches a server through
// the operator's ssh, and the tracer half of its trace fetch.
//
//   - target.go: what one pod session is (context, namespace, pod, app
//     container, image, mode, profile), how flags, the pod entry, the
//     cluster and the defaults layer into it (#18's precedence), and the
//     checks every value passes before it reaches a kubectl argv: a
//     context the registry lists, an image pinned by digest, Kubernetes
//     names only.
//   - kubectl.go: the Transport — Run is `kubectl exec`, Fetch an
//     exec-streamed tar of the project's ready/ segments, Push refused
//     (the image carries the tracer; #12 rules out `kubectl cp`).
//   - debug.go: `kubectl debug --target`, the ephemeral tracer container,
//     created only in a pod already prepared with #13's two volumes.
//   - ready.go: both ends of the fetch stream — the tracer's
//     `iormesh ready` writer (cgo-free, so internal/cli wires it in) and
//     the controller's unpacker, which trusts nothing in the stream.
//
// No call ever goes through a shell: kubectl gets an argv, and
// `kubectl exec … -- argv` hands the container runtime an argv too, so
// pod or container names cannot inject anything; they are checked
// against Kubernetes' own name rules regardless, and every value goes in
// `--flag=value` form so none can read as an option.
//
// The package is cgo-free: iormeshctl runs on a laptop (CGO_ENABLED=0).
package k8s

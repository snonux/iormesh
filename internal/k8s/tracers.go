package k8s

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// A debug pod's tracers over its life (task ah4). `kubectl debug
// --target` joins the app container's PID namespace, so when the app
// container's init dies (a crash, an OOM kill, a liveness kill) the
// kernel kills the tracer with it, and the kubelet never restarts an
// ephemeral container. Nor can one be removed or reused by name: the
// name stays in the pod's spec until the pod goes. So Deploy replaces an
// ended tracer with a fresh one under the next name — "iormesh",
// "iormesh-2", … up to MaxDebugTracers — and every later exec (collect,
// the attach wait) names the newest. The trace emptyDir is the pod's,
// shared by all of them, so the newest tracer's `iormesh ready` serves
// what its predecessors published; only a predecessor's unsealed
// segment (.active-*, swept by the next writer) is lost. A sidecar is
// always plain "iormesh": with shareProcessNamespace the pause
// container is the namespace's init and the tracer outlives app
// restarts.

// MaxDebugTracers caps the debug tracers one pod may carry. Each
// replaced one stays in the pod's spec and status for good, so a pod
// whose app keeps restarting would otherwise collect them without end;
// past the cap, deploy refuses and says to use sidecar mode or restart
// the pod.
const MaxDebugTracers = 8

// TracerName is the name of a pod's n-th debug tracer (1-based):
// TracerContainer for the first, TracerContainer-n after it. Only names
// tracerIndex accepts back are ever made: n in 1..MaxDebugTracers.
func TracerName(n int) string {
	if n <= 1 {
		return TracerContainer
	}
	return TracerContainer + "-" + strconv.Itoa(n)
}

// tracerIndex is the n of a debug tracer name TracerName makes, and
// whether name is one: "iormesh" is 1, "iormesh-2" … "iormesh-8" are
// 2 … MaxDebugTracers, written without a sign or leading zero. Anything
// else — "iormesh-1", "iormesh-02", "iormesh-9" — is not iormeshctl's
// and is neither counted, reported nor exec'd into.
func tracerIndex(name string) (int, bool) {
	if name == TracerContainer {
		return 1, true
	}
	num, ok := strings.CutPrefix(name, TracerContainer+"-")
	if !ok || num == "" || num[0] == '0' || strings.Trim(num, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(num)
	if err != nil || n < 2 || n > MaxDebugTracers {
		return 0, false
	}
	return n, true
}

// debugTracers lists the pod's debug tracer containers by index, the
// newest last: every ephemeral container with a name tracerIndex
// accepts.
func debugTracers(cs []named) []named {
	var out []named
	for _, c := range cs {
		if _, ok := tracerIndex(c.Name); ok {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b named) int {
		i, _ := tracerIndex(a.Name)
		j, _ := tracerIndex(b.Name)
		return i - j
	})
	return out
}

// statusOf finds container name's status in statuses.
func statusOf(statuses []containerStatus, name string) (containerStatus, bool) {
	i := slices.IndexFunc(statuses, func(s containerStatus) bool { return s.Name == name })
	if i < 0 {
		return containerStatus{}, false
	}
	return statuses[i], true
}

// ended reports whether the container's status says it terminated: the
// only state in which an ephemeral container stays down for good.
func ended(statuses []containerStatus, name string) bool {
	st, ok := statusOf(statuses, name)
	return ok && st.State.Terminated != nil
}

// nextTracer is the name Deploy gives a fresh debug tracer in place of
// the ended ones in ts (one tracer per pod, PLAN.md M7): every one of
// them must have terminated — one running, waiting or without a status
// yet is still the pod's tracer, and a second beside it would write a
// second session under the same pod identity — and the pod must have
// room under MaxDebugTracers.
func (k *Kubectl) nextTracer(ts []named, statuses []containerStatus) (string, error) {
	for _, c := range ts {
		if !ended(statuses, c.Name) {
			why := "without a status yet"
			if st, ok := statusOf(statuses, c.Name); ok {
				why = cmp.Or(st.notRunning(), "running")
			}
			return "", fmt.Errorf("%w: %s/%s: debug tracer %q is %s beside the newer %q; one tracer per pod: "+
				"restart the pod", ErrTransport, k.t.Namespace, k.t.Pod, c.Name, why, ts[len(ts)-1].Name)
		}
	}
	n, _ := tracerIndex(ts[len(ts)-1].Name)
	if n >= MaxDebugTracers {
		return "", fmt.Errorf("%w: %s/%s: debug tracer %q ended and the pod has had %d, the most deploy "+
			"starts in one pod (ephemeral containers are never removed); restart the pod, or use sidecar mode "+
			"for a pod whose app container restarts", ErrTransport, k.t.Namespace, k.t.Pod,
			ts[len(ts)-1].Name, MaxDebugTracers)
	}
	return TracerName(n + 1), nil
}

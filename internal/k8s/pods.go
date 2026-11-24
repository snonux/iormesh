package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"iormesh/internal/k8sname"
)

// Pod lookup for the registry's pods: entries (decision #18, task af4): an
// entry names its pod or a label selector, and the controller asks the
// API server — through the operator's kubectl, as for everything else —
// which running pods that is, and for each its UID (the store directory
// and host identity) and whether a tracer container is in it.

// Lookup errors, for errors.Is.
var (
	// ErrNoPod: the entry's pod is not running, or its selector matches
	// no running pod. Never "nothing to collect": a pod that is gone
	// took its emptyDir, and the operator must hear of it.
	ErrNoPod = errors.New("k8s: no running pod")
	// ErrTooManyPods: a selector matches more running pods than
	// MaxSelected — likely a selector broader than meant.
	ErrTooManyPods = errors.New("k8s: selector matches too many pods")
)

// MaxSelected bounds how many pods one selector may pick. A selector
// picks every running pod it matches (a Deployment's replicas are one
// service, traced together), each its own session; past this many the
// entry is refused rather than tracing half a namespace by accident.
const MaxSelected = 32

// podInfo is the part of `kubectl get pod -o json` the checks read.
type podInfo struct {
	Metadata struct {
		Name string `json:"name"`
		UID  string `json:"uid"`
		// DeletionTimestamp is set once the pod is being deleted.
		DeletionTimestamp string `json:"deletionTimestamp"`
	} `json:"metadata"`
	Spec struct {
		HostUsers             *bool `json:"hostUsers"`
		ShareProcessNamespace *bool `json:"shareProcessNamespace"`
		Volumes               []struct {
			Name     string `json:"name"`
			HostPath *struct {
				Path string `json:"path"`
			} `json:"hostPath"`
			EmptyDir *struct {
				SizeLimit string `json:"sizeLimit"`
			} `json:"emptyDir"`
		} `json:"volumes"`
		Containers          []named `json:"containers"`
		EphemeralContainers []named `json:"ephemeralContainers"`
	} `json:"spec"`
	Status struct {
		Phase                      string            `json:"phase"`
		ContainerStatuses          []containerStatus `json:"containerStatuses"`
		EphemeralContainerStatuses []containerStatus `json:"ephemeralContainerStatuses"`
	} `json:"status"`
}

// named is a container entry: its name, its spec image (which Deploy
// checks for an earlier debug container, N6) and its command line (whose
// -project Deploy checks for one, task 5g4).
type named struct {
	Name    string   `json:"name"`
	Image   string   `json:"image"`
	Command []string `json:"command"`
	Args    []string `json:"args"`
}

// containerStatus is a container's status: the image it really runs
// (imageID, resolved by the kubelet) and its state.
type containerStatus struct {
	Name    string `json:"name"`
	ImageID string `json:"imageID"`
	State   struct {
		// Running.StartedAt is when the kubelet started the container
		// (RFC3339, whole seconds): WaitAttached's line between a
		// predecessor's state.json and the new run's (task eh4).
		Running *struct {
			StartedAt string `json:"startedAt"`
		} `json:"running"`
		Terminated *struct {
			Reason   string `json:"reason"`
			ExitCode int    `json:"exitCode"`
		} `json:"terminated"`
		Waiting *struct {
			Reason string `json:"reason"`
		} `json:"waiting"`
	} `json:"state"`
}

// notRunning describes a status that is not running ("" when it is): a
// terminated container with its reason and exit code, or a waiting one.
func (st containerStatus) notRunning() string {
	switch s := st.State; {
	case s.Running != nil:
		return ""
	case s.Terminated != nil:
		return fmt.Sprintf("terminated (%s, exit code %d)", s.Terminated.Reason, s.Terminated.ExitCode)
	case s.Waiting != nil:
		return fmt.Sprintf("waiting (%s)", s.Waiting.Reason)
	}
	return "in no known state"
}

// hasContainer reports whether cs holds a container called name.
func hasContainer(cs []named, name string) bool {
	return slices.ContainsFunc(cs, func(c named) bool { return c.Name == name })
}

// PodState is what the controller needs of one pod before it reaches it.
type PodState struct {
	Name string
	// UID is the pod's UID: its store directory and host identity
	// (PodHostIdentity), so a pod recreated under the same name — a
	// StatefulSet's — is a new host, never the old one's continuation.
	UID string
	// Tracer: the pod has the mode's tracer container (the sidecar, or
	// an ephemeral container from an earlier deploy). A pod without one
	// has nothing to collect — said by the API server, not guessed from
	// a failed exec.
	Tracer bool
	// Container is the tracer container every exec names ("" without
	// one): the sidecar's TracerContainer, or the newest debug tracer
	// (TracerName, task ah4) — the one deploy left running or started
	// last. Its predecessors ended; whatever they published is in the
	// shared emptyDir, which the newest one's `iormesh ready` serves.
	Container string
	// Stopped says why that tracer container is not running ("" while
	// it runs): a debug session deployed with -linger stays running
	// after it ends, so a stopped one crashed, was OOM-killed, was
	// killed with its app container (debug mode joins that container's
	// PID namespace, task ah4) or predates -linger, and `kubectl exec`
	// cannot enter it — its segments in the emptyDir are out of reach
	// until a deploy starts the next debug tracer or the pod goes
	// (review B1).
	Stopped string
	// Exited is set when that tracer container terminated, ExitCode
	// then its exit code. iormeshctl ensure (task pk4) reads it to tell
	// a debug tracer ended on purpose — exit code 0: a -linger run
	// exits only once SIGTERM or SIGINT ends it, cleanly — from one
	// killed with its app container, OOM-killed or crashed.
	Exited   bool
	ExitCode int
}

// Lister looks pods up in one scope's context and namespace.
type Lister struct {
	s   Scope
	run runner
}

// NewLister returns the lister for s, validated again.
func NewLister(s Scope) (*Lister, error) {
	return newLister(s, execRunner{bin: "kubectl"})
}

// newLister is NewLister with the command runner injected.
func newLister(s Scope, r runner) (*Lister, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	return &Lister{s: s, run: r}, nil
}

// get runs `kubectl get` with args, every call carrying the scope's
// context and namespace, and decodes the JSON into v. Each get runs under
// callTimeout (boundedOutput, task pi4).
func (l *Lister) get(ctx context.Context, v any, args ...string) error {
	argv := slices.Concat([]string{"--context=" + l.s.Context, "--namespace=" + l.s.Namespace, "get"},
		args, []string{"--output=json"})
	out, err := boundedOutput(ctx, l.run, argv)
	if err != nil {
		return fmt.Errorf("kubectl get %s in %s: %w", strings.Join(args, " "), l.s.Namespace, err)
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("kubectl get %s in %s: %w", strings.Join(args, " "), l.s.Namespace, err)
	}
	return nil
}

// Get is the pod called name, refused unless it is running: a pod that
// is pending or finished cannot be exec'd into, and that is the
// operator's to know, not an empty collect. A pod kubectl cannot read
// (gone, forbidden) is an error too.
func (l *Lister) Get(ctx context.Context, name string) (PodState, error) {
	if !k8sname.Pod(name) {
		return PodState{}, fmt.Errorf("%w: pod %q", ErrName, name)
	}
	var p podInfo
	if err := l.get(ctx, &p, "pod", name); err != nil {
		return PodState{}, err
	}
	if p.Status.Phase != "Running" {
		return PodState{}, fmt.Errorf("%w: %s/%s is %q", ErrNoPod, l.s.Namespace, name, p.Status.Phase)
	}
	return l.state(p)
}

// Select is every running, not terminating pod selector matches, sorted
// by name. None is ErrNoPod, more than MaxSelected ErrTooManyPods
// (PLAN.md #18).
func (l *Lister) Select(ctx context.Context, selector string) ([]PodState, error) {
	if !k8sname.Selector(selector) {
		return nil, fmt.Errorf("%w: selector %q (k=v[,k!=v...], one = at least)", ErrName, selector)
	}
	var list struct {
		Items []podInfo `json:"items"`
	}
	if err := l.get(ctx, &list, "pods", "--selector="+selector); err != nil {
		return nil, err
	}
	var pods []PodState
	for _, p := range list.Items {
		// A terminating pod (deletionTimestamp set) still reports phase
		// Running until its containers stop: right after a rollout — the
		// prepare patch's own — a selector matched the old, unprepared
		// pod too, and deploy failed on it (kind, task yb4). Its tracer,
		// if any, ends on the kubelet's SIGTERM within the grace period,
		// so neither deploy nor collect has anything to do there.
		if p.Status.Phase != "Running" || p.Metadata.DeletionTimestamp != "" {
			continue
		}
		st, err := l.state(p)
		if err != nil {
			return nil, err
		}
		pods = append(pods, st)
	}
	switch {
	case len(pods) == 0:
		return nil, fmt.Errorf("%w: selector %q matches %d pods in %s, none running", ErrNoPod,
			selector, len(list.Items), l.s.Namespace)
	case len(pods) > MaxSelected:
		return nil, fmt.Errorf("%w: selector %q matches %d running pods in %s, at most %d", ErrTooManyPods,
			selector, len(pods), l.s.Namespace, MaxSelected)
	}
	slices.SortFunc(pods, func(a, b PodState) int { return strings.Compare(a.Name, b.Name) })
	return pods, nil
}

// state checks a pod's name and UID — both reach a kubectl argv or a
// local path — and notes the mode's tracer container in it, if any: the
// sidecar, or the newest debug tracer (debugTracers).
func (l *Lister) state(p podInfo) (PodState, error) {
	if !k8sname.Pod(p.Metadata.Name) || !k8sname.UID(p.Metadata.UID) {
		return PodState{}, fmt.Errorf("%w: pod %q uid %q", ErrName, p.Metadata.Name, p.Metadata.UID)
	}
	var name string
	statuses := p.Status.EphemeralContainerStatuses
	if l.s.Mode == ModeSidecar {
		if hasContainer(p.Spec.Containers, TracerContainer) {
			name = TracerContainer
		}
		statuses = p.Status.ContainerStatuses
	} else if ts := debugTracers(p.Spec.EphemeralContainers); len(ts) > 0 {
		name = ts[len(ts)-1].Name
	}
	st := PodState{Name: p.Metadata.Name, UID: p.Metadata.UID, Tracer: name != "", Container: name}
	if st.Tracer {
		cs, ok := statusOf(statuses, name)
		if st.Stopped = "without a status yet"; ok {
			st.Stopped = cs.notRunning()
			if t := cs.State.Terminated; t != nil {
				st.Exited, st.ExitCode = true, t.ExitCode
			}
		}
	}
	return st, nil
}

// PodHostID is the host_id a tracer in the pod with this UID reports
// (task 7f4's advisor decision: "pod-" + POD_UID, never the node's or the
// image's machine-id), and the controller's store directory name for the
// pod: one directory per pod incarnation.
func PodHostID(uid string) string { return k8sname.PodHostID(uid) }

// PodFQDN is the FQDN a tracer in the pod reports (the same decision):
// <name>.<namespace>.<first 8 hex of the UID>, no resolver lookup.
func PodFQDN(name, namespace, uid string) string {
	return name + "." + namespace + "." + uid[:min(8, len(uid))]
}

package ctl

import (
	"context"
	"flag"
	"fmt"
	"path"
	"slices"

	"iormesh/internal/federate"
	"iormesh/internal/k8s"
)

// Pods as targets (PLAN.md M7, decision #18, task af4). The registry's
// pods: entries are named on the command line by their reference,
// <cluster>/<namespace>/<pod or selector> (federate.Registry.PodRef):
// the "/" tells one from a host, which may not contain it. Every pod is
// reached through the operator's kubectl (internal/k8s), with the
// context the registry lists, never the kubeconfig's current one:
//
//   - collect: the API server says which running pods the entry is
//     (the named pod, or every pod its selector matches, internal/k8s
//     Lister) and whether each has a tracer container; each pod with one
//     is fetched through `kubectl exec … iormesh ready` (k8s.Kubectl's
//     Fetch). None of the SSH path's probes run — `true`, `test -d` and
//     `find` are not in the scratch image: the API answers for
//     reachability and presence, a pod kubectl cannot reach fails the
//     fetch (ErrUnreachable, never an empty stream), and a ready/ that
//     does not exist yet is an empty stream from the tracer itself.
//   - deploy: per pod, k8s.Kubectl.Deploy — a prepared pod gets an
//     ephemeral tracer running the entry's project, a pod whose tracer
//     already runs the pinned image is left alone, a pod whose debug
//     tracers all ended (killed with a restarting app container) gets
//     the next one, "iormesh-2" and on (task ah4), a sidecar is checked
//     for the pinned image. No uname, command -v, mkdir or Push: the
//     image carries the tracer, pinned by digest.
//   - rm: the entry's projects are collected one last time, then the
//     entry leaves the registry. Nothing is stopped or uninstalled: an
//     ephemeral container can be neither removed nor signalled from a
//     shell-less image, so the session runs on until its -duration ends
//     or the pod goes, and a sidecar until its patch is reverted.
//
// Each pod is its own store directory, "pod-<uid>" (k8s.PodHostID): the
// UID, not the name, since a StatefulSet pod recreated under its name is
// a new pod with an empty emptyDir and a new host identity. Collect pins
// the directory to that identity (hosts.json, federate.vetHosts) from
// the API server's answer, before any segment arrives.

// k8sFlags are the per-run overrides of #18: flag > pod entry > cluster
// > k8s: defaults.
type k8sFlags struct {
	context, namespace, image string
	// seType is -selinux-type: nil unless given, so an unset flag defers
	// to the registry while -selinux-type '' omits the field.
	seType *string
	// seLevel is -selinux-level, nil unless given likewise (task 8h4).
	seLevel *string
}

// optString is a string flag that tells "not given" (nil) from "given
// empty".
type optString struct{ v **string }

func (o optString) String() string {
	if o.v == nil || *o.v == nil {
		return ""
	}
	return **o.v
}

func (o optString) Set(s string) error {
	*o.v = &s
	return nil
}

// addK8sFlags registers -context and -namespace, and -image,
// -selinux-type and -selinux-level when the command starts tracers
// (deploy).
func addK8sFlags(fs *flag.FlagSet, deploy bool) *k8sFlags {
	f := &k8sFlags{}
	fs.StringVar(&f.context, "context", "", "kubectl context for pod entries (must be a registered cluster's)")
	fs.StringVar(&f.namespace, "namespace", "", "namespace for pod entries, over the entry's and the cluster's")
	if deploy {
		fs.StringVar(&f.image, "image", "", "tracer image for pod entries, pinned by digest")
		fs.Var(optString{&f.seType}, "selinux-type",
			"SELinux type of a minimal-profile debug tracer (default "+k8s.DefaultSELinuxType+
				"; empty omits seLinuxOptions)")
		fs.Var(optString{&f.seLevel}, "selinux-level",
			"MCS level beside -selinux-type: s0 or the pod's pinned s0:cN,cM (default "+
				k8s.DefaultSELinuxLevel+"; empty omits the level)")
	}
	return f
}

// refuse fails when -context or -namespace is set where an override
// would be ambiguous (review N1): rm, which edits the registry by the
// entry's own reference and must collect what the entry names, and a
// collect or refresh of every target, which would point every entry of
// every cluster at one namespace.
func (f *k8sFlags) refuse(where string) error {
	if f.context != "" || f.namespace != "" {
		return fmt.Errorf("%w: -context and -namespace are not taken by %s; name the pod entry instead",
			errUsage, where)
	}
	return nil
}

// podEntry is a pods: entry resolved for this run.
type podEntry struct {
	ref      string
	entry    federate.Pod
	scope    k8s.Scope
	projects []federate.Project
}

// resolvePodRef finds ref in the registry and layers the run's flags,
// the entry, its cluster and the k8s: defaults into its scope. A context
// no cluster lists, an unpinned image or a bad name fails here, before
// anything is contacted.
func resolvePodRef(r *federate.Registry, ref string, kf *k8sFlags) (podEntry, error) {
	p, err := r.LookupPod(ref)
	if err != nil {
		return podEntry{}, err
	}
	c, _ := r.Cluster(p.Cluster)
	// An override may restate the entry's cluster's context, never move
	// the entry to another cluster (review N1): a pod entry's name and
	// its store directories stay tied to the cluster it names.
	if kf.context != "" && kf.context != c.Context {
		return podEntry{}, fmt.Errorf("pod entry %s: %w: -context %q is not cluster %s's (%q)", ref,
			k8s.ErrContext, kf.context, c.Name, c.Context)
	}
	var defaults k8s.Layer
	if r.K8s != nil {
		defaults = k8s.Layer{Image: r.K8s.Image, Mode: k8s.Mode(r.K8s.Mode), Profile: k8s.Profile(r.K8s.Profile),
			SELinuxType: r.K8s.SELinuxType, SELinuxLevel: r.K8s.SELinuxLevel}
	}
	s, err := k8s.ResolveScope(r.Contexts(),
		k8s.Layer{Context: kf.context, Namespace: kf.namespace, Image: kf.image, SELinuxType: kf.seType,
			SELinuxLevel: kf.seLevel},
		k8s.Layer{Namespace: p.Namespace, Image: p.Image, Mode: k8s.Mode(p.Mode), Profile: k8s.Profile(p.Profile),
			SELinuxType: p.SELinuxType, SELinuxLevel: p.SELinuxLevel},
		k8s.Layer{Context: c.Context, Namespace: c.Namespace},
		defaults)
	if err != nil {
		return podEntry{}, fmt.Errorf("pod entry %s: %w", ref, err)
	}
	return podEntry{ref: ref, entry: p, scope: s, projects: r.PodProjects(p)}, nil
}

// pods asks the API server which running pods the entry is now.
func (pe podEntry) pods(ctx context.Context) ([]k8s.PodState, error) {
	l, err := k8s.NewLister(pe.scope)
	if err != nil {
		return nil, err
	}
	if pe.entry.Pod != "" {
		st, err := l.Get(ctx, pe.entry.Pod)
		if err != nil {
			return nil, err
		}
		return []k8s.PodState{st}, nil
	}
	return l.Select(ctx, pe.entry.Selector)
}

// transport is the kubectl transport into one of the entry's pods, its
// execs naming the tracer container tracer (PodState.Container: the
// newest debug tracer, task ah4), or k8s.TracerContainer when "" —
// deploy's case, where Deploy picks the container itself.
func (pe podEntry) transport(pod, tracer string) (*k8s.Kubectl, error) {
	t, err := pe.scope.Target(pod, pe.entry.Container)
	if err != nil {
		return nil, err
	}
	t.Tracer = tracer
	return k8s.New(t)
}

// label names a pod in output: the entry's reference and the pod.
func (pe podEntry) label(pod string) string {
	return pe.ref + " pod " + pod
}

// podIdentity is the host identity a tracer in st reports (task 7f4's
// decision, k8s.PodHostID and PodFQDN), which collect pins its store
// directory to.
func (pe podEntry) podIdentity(st k8s.PodState) federate.HostIdentity {
	return federate.HostIdentity{HostID: k8s.PodHostID(st.UID),
		FQDN: k8s.PodFQDN(st.Name, pe.scope.Namespace, st.UID)}
}

// podDiskFlags are the disk bounds a debug deploy derives from the pod
// itself (k8s.PodDiskArgs, task df4): the byte cap from the trace
// emptyDir's sizeLimit, and no percent limit, since statfs in a pod sees
// the node's partition. The registry may not set them: one passed
// through would be overridden by the pod's anyway, silently.
var podDiskFlags = map[string]bool{"disk-limit": true, "disk-max-bytes": true}

// checkPodFlags refuses, before any pod is contacted, pass-through flags
// the entry sets itself (ownedFlags), the disk bounds deploy takes from
// the pod (podDiskFlags) and a -trace-dir other than the pod's emptyDir,
// where collect fetches from.
func checkPodFlags(p federate.Project) error {
	if f := firstOwnedFlag(p.Flags); f != "" {
		return fmt.Errorf("flag %s: the entry sets it itself", f)
	}
	for i, f := range p.Flags {
		key, val, hasVal := cutFlag(f)
		if podDiskFlags[key] {
			return fmt.Errorf("flag %s: deploy sets it from the pod's %s sizeLimit", f, k8s.TracesVolume)
		}
		if key != "trace-dir" {
			continue
		}
		if !hasVal && i+1 < len(p.Flags) {
			val = p.Flags[i+1]
		}
		if path.Clean(val) != k8s.TraceRoot {
			return fmt.Errorf("flag %s: a pod's session may only use %s", f, k8s.TraceRoot)
		}
	}
	return nil
}

// podRunArgs is the tracer argv a debug container runs for p: start's
// argv (startArgs) as a foreground run — the container is the unit — of
// the image's tracer, with -linger: when the session's -duration ends,
// the run frees its BPF, publishes its last segment and waits instead of
// exiting, since an exited ephemeral container can never be exec'd into
// again and collect would lose that last segment (review B1).
func podRunArgs(p federate.Project) []string {
	argv := startArgs(p)
	argv[0] = "run"
	argv = slices.Insert(argv, 3, "-linger")
	return append([]string{k8s.TracerBinary}, argv...)
}

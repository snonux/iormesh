package federate

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"iormesh/internal/k8sname"
)

// The registry's Kubernetes sections (docs/milestones.md M7, decision #18 —
// provisional, the owner's examples pending; task af4): k8s: holds the
// defaults and the clusters, pods: the pod entries, each with the same
// projects: as a server. Only what is listed here is ever reached
// (principle #3): a pod entry names a registered cluster, and the
// clusters' contexts are the only ones kubectl is ever given.

// ErrUnknownPod: a pod reference no pods: entry carries.
var ErrUnknownPod = errors.New("registry: pod entry not registered")

// The mode and profile values (internal/k8s Mode and Profile; repeated
// here because internal/k8s imports this package).
const (
	modeDebug         = "debug"
	modeSidecar       = "sidecar"
	profileMinimal    = "minimal"
	profilePrivileged = "privileged"
)

// K8s is the k8s: section: defaults for every pod entry, and the
// clusters pod entries name.
type K8s struct {
	Image   string `yaml:"image,omitempty"`
	Mode    string `yaml:"mode,omitempty"`
	Profile string `yaml:"profile,omitempty"`
	// SELinuxType is the minimal profile's seLinuxOptions.type (decision
	// #13, task 5h4; internal/k8s DefaultSELinuxType when unset). A
	// pointer, since an explicit "" is a choice: omit seLinuxOptions.
	SELinuxType *string `yaml:"selinux_type,omitempty"`
	// SELinuxLevel is the MCS level beside it (task 8h4; internal/k8s
	// DefaultSELinuxLevel, s0, when unset): an owner who pins pod levels
	// passes the pod's pair. A pointer too: "" omits the level.
	SELinuxLevel *string   `yaml:"selinux_level,omitempty"`
	Clusters     []Cluster `yaml:"clusters"`
}

// Cluster is a label for one kubeconfig context and its default
// namespace (which is also the default project, decision #21).
type Cluster struct {
	Name      string `yaml:"name"`
	Context   string `yaml:"context"`
	Namespace string `yaml:"namespace,omitempty"`
}

// Pod is one pods: entry: a pod by name, or the running pods a label
// selector picks, in a cluster's namespace; the app container kubectl
// debug targets; optional image, mode, profile, SELinux type and level
// over the k8s: ones; and its projects. A project's name may be left out: it is then the
// namespace (decision #21, PodProjects).
type Pod struct {
	Cluster   string `yaml:"cluster"`
	Namespace string `yaml:"namespace,omitempty"`
	Selector  string `yaml:"selector,omitempty"`
	Pod       string `yaml:"pod,omitempty"`
	Container string `yaml:"container"`
	Image     string `yaml:"image,omitempty"`
	Mode      string `yaml:"mode,omitempty"`
	Profile   string `yaml:"profile,omitempty"`
	// SELinuxType overrides K8s.SELinuxType for this entry.
	SELinuxType *string `yaml:"selinux_type,omitempty"`
	// SELinuxLevel overrides K8s.SELinuxLevel for this entry: the pod's
	// own pinned pair, typically.
	SELinuxLevel *string   `yaml:"selinux_level,omitempty"`
	Projects     []Project `yaml:"projects"`
}

// IsPodRef reports whether a command-line target names a pod entry
// rather than a host: a pod reference has a "/", which no registry host
// name may contain.
func IsPodRef(arg string) bool { return strings.Contains(arg, "/") }

// Contexts lists the kubeconfig contexts the registry's clusters name:
// the only ones a run may use, its -context flag included.
func (r *Registry) Contexts() []string {
	if r.K8s == nil {
		return nil
	}
	var out []string
	for _, c := range r.K8s.Clusters {
		out = append(out, c.Context)
	}
	return out
}

// Cluster returns the cluster called name.
func (r *Registry) Cluster(name string) (Cluster, bool) {
	if r.K8s == nil {
		return Cluster{}, false
	}
	i := slices.IndexFunc(r.K8s.Clusters, func(c Cluster) bool { return c.Name == name })
	if i < 0 {
		return Cluster{}, false
	}
	return r.K8s.Clusters[i], true
}

// PodNamespace is p's namespace: its own, else its cluster's.
func (r *Registry) PodNamespace(p Pod) string {
	if p.Namespace != "" {
		return p.Namespace
	}
	c, _ := r.Cluster(p.Cluster)
	return c.Namespace
}

// PodMode is p's mode: its own, else the k8s: default, else debug.
func (r *Registry) PodMode(p Pod) string {
	switch {
	case p.Mode != "":
		return p.Mode
	case r.K8s != nil && r.K8s.Mode != "":
		return r.K8s.Mode
	}
	return modeDebug
}

// PodRef is how commands name p: <cluster>/<namespace>/<pod>, or
// <cluster>/<namespace>/<selector> for a selector entry.
func (r *Registry) PodRef(p Pod) string {
	return p.Cluster + "/" + r.PodNamespace(p) + "/" + p.Pod + p.Selector
}

// PodProjects is p's projects with each left-out name defaulted to the
// pod's namespace (decision #21).
func (r *Registry) PodProjects(p Pod) []Project {
	out := slices.Clone(p.Projects)
	for i := range out {
		if out[i].Name == "" {
			out[i].Name = r.PodNamespace(p)
		}
	}
	return out
}

// LookupPod returns the entry ref names, or ErrUnknownPod.
func (r *Registry) LookupPod(ref string) (Pod, error) {
	i := r.findPod(ref)
	if i < 0 {
		return Pod{}, fmt.Errorf("%w: %s", ErrUnknownPod, ref)
	}
	return r.Pods[i], nil
}

// RemovePod drops the entry ref names, or only its project name when
// name is set; an entry whose last project goes is dropped too.
func (r *Registry) RemovePod(ref, name string) error {
	i := r.findPod(ref)
	if i < 0 {
		return fmt.Errorf("%w: %s", ErrUnknownPod, ref)
	}
	if name != "" {
		projects := r.PodProjects(r.Pods[i])
		j := slices.IndexFunc(projects, func(p Project) bool { return p.Name == name })
		if j < 0 {
			return fmt.Errorf("%w: %q on %s", ErrUnknownProject, name, ref)
		}
		r.Pods[i].Projects = slices.Delete(r.Pods[i].Projects, j, j+1)
		if len(r.Pods[i].Projects) > 0 {
			return nil
		}
	}
	r.Pods = slices.Delete(r.Pods, i, i+1)
	return nil
}

// findPod returns the index of the entry ref names, or -1.
func (r *Registry) findPod(ref string) int {
	return slices.IndexFunc(r.Pods, func(p Pod) bool { return r.PodRef(p) == ref })
}

// validateK8s checks the k8s: and pods: sections.
func (r *Registry) validateK8s() error {
	if r.K8s != nil {
		if err := r.K8s.validate(); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, p := range r.Pods {
		if err := r.validatePod(p); err != nil {
			return err
		}
		ref := r.PodRef(p)
		if seen[ref] {
			return fmt.Errorf("%w: pod entry %s listed twice", ErrInvalid, ref)
		}
		seen[ref] = true
	}
	return nil
}

func (k *K8s) validate() error {
	if err := checkPodOptions("k8s:", k.Image, k.Mode, k.Profile, k.SELinuxType, k.SELinuxLevel); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, c := range k.Clusters {
		switch {
		case !k8sname.Label(c.Name):
			return fmt.Errorf("%w: cluster name %q: want a DNS label", ErrInvalid, c.Name)
		case seen[c.Name]:
			return fmt.Errorf("%w: cluster %q listed twice", ErrInvalid, c.Name)
		case !k8sname.Context(c.Context):
			return fmt.Errorf("%w: cluster %s: context %q", ErrInvalid, c.Name, c.Context)
		case c.Namespace != "" && !k8sname.Label(c.Namespace):
			return fmt.Errorf("%w: cluster %s: namespace %q", ErrInvalid, c.Name, c.Namespace)
		}
		seen[c.Name] = true
	}
	return nil
}

// checkPodOptions checks an image (pinned by digest when set), a mode
// and a profile (each empty or a known value), an SELinux type (unset,
// empty, or a type name) and an MCS level (unset, empty, s0 or
// s0:cN,cM).
func checkPodOptions(where, image, mode, prof string, seType, seLevel *string) error {
	switch {
	case image != "" && !k8sname.Pinned(image):
		return fmt.Errorf("%w: %s image %q is not pinned by digest (<repo>@sha256:<64 hex>)", ErrInvalid, where, image)
	case mode != "" && mode != modeDebug && mode != modeSidecar:
		return fmt.Errorf("%w: %s mode %q (debug or sidecar)", ErrInvalid, where, mode)
	case prof != "" && prof != profileMinimal && prof != profilePrivileged:
		return fmt.Errorf("%w: %s profile %q (minimal or privileged)", ErrInvalid, where, prof)
	case seType != nil && *seType != "" && !k8sname.SELinuxType(*seType):
		return fmt.Errorf("%w: %s selinux_type %q ([a-z0-9_]+, or empty to omit it)", ErrInvalid, where, *seType)
	case seLevel != nil && *seLevel != "" && !k8sname.SELinuxLevel(*seLevel):
		return fmt.Errorf("%w: %s selinux_level %q (s0 or s0:cN,cM with N, M <= 1023, or empty to omit it)",
			ErrInvalid, where, *seLevel)
	}
	return nil
}

// validatePod checks one pods: entry.
func (r *Registry) validatePod(p Pod) error {
	where := "pod entry " + p.Cluster + "/" + p.Pod + p.Selector
	if _, ok := r.Cluster(p.Cluster); !ok {
		return fmt.Errorf("%w: %s: cluster %q is not in k8s.clusters", ErrInvalid, where, p.Cluster)
	}
	if err := checkPodTarget(where, p, r.PodNamespace(p)); err != nil {
		return err
	}
	if err := checkPodOptions(where+":", p.Image, p.Mode, p.Profile, p.SELinuxType, p.SELinuxLevel); err != nil {
		return err
	}
	projects := r.PodProjects(p)
	if len(projects) == 0 {
		return fmt.Errorf("%w: %s has no project", ErrInvalid, where)
	}
	// An ephemeral container runs one process, one tracer session: a
	// debug-mode entry carries exactly one project.
	if r.PodMode(p) == modeDebug && len(projects) > 1 {
		return fmt.Errorf("%w: %s: debug mode runs one session per pod, not %d projects", ErrInvalid,
			where, len(projects))
	}
	return validateProjects(where, projects)
}

// checkPodTarget checks the entry's pod or selector, namespace and app
// container. A namespace is a DNS label, so it is always a valid project
// label too (PodProjects' default).
func checkPodTarget(where string, p Pod, ns string) error {
	switch {
	case (p.Pod == "") == (p.Selector == ""):
		return fmt.Errorf("%w: %s: want exactly one of pod: and selector:", ErrInvalid, where)
	case p.Pod != "" && !k8sname.Pod(p.Pod):
		return fmt.Errorf("%w: %s: pod name %q", ErrInvalid, where, p.Pod)
	case p.Selector != "" && !k8sname.Selector(p.Selector):
		return fmt.Errorf("%w: %s: selector %q (k=v[,k!=v...], one = at least)", ErrInvalid, where, p.Selector)
	case !k8sname.Label(ns):
		return fmt.Errorf("%w: %s: namespace %q (set it on the entry or its cluster)", ErrInvalid, where, ns)
	case !k8sname.Label(p.Container) || p.Container == "iormesh":
		return fmt.Errorf("%w: %s: app container %q", ErrInvalid, where, p.Container)
	}
	return nil
}

// validateProjects checks projects are valid and unique.
func validateProjects(where string, projects []Project) error {
	seen := map[string]bool{}
	for _, p := range projects {
		if seen[p.Name] {
			return fmt.Errorf("%w: project %q listed twice on %s", ErrInvalid, p.Name, where)
		}
		seen[p.Name] = true
		if err := p.validate(); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
	}
	return nil
}

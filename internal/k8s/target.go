package k8s

import (
	"errors"
	"fmt"
	"slices"

	"iormesh/internal/federate"
	"iormesh/internal/k8sname"
)

// Errors a Target or a kubectl call is refused with, for errors.Is.
var (
	// ErrContext: the kubectl context is malformed or not one the
	// registry lists (#18: the registry, never the kubeconfig's current
	// context, picks the cluster).
	ErrContext = errors.New("k8s: context refused")
	// ErrImage: the tracer image is not pinned by digest.
	ErrImage = errors.New("k8s: image refused")
	// ErrName: a namespace, pod or container name is not a Kubernetes
	// name.
	ErrName = errors.New("k8s: name refused")
	// ErrUnprepared: the pod lacks #13's volumes (or, for a sidecar,
	// the tracer container), so no tracer can run in it.
	ErrUnprepared = errors.New("k8s: pod not prepared")
	// ErrTransport: an argument or operation the transport refuses
	// before running anything. It wraps federate.ErrTransport, so a
	// caller holding any Transport can test for either.
	ErrTransport = fmt.Errorf("k8s: %w", federate.ErrTransport)
	// ErrUnreachable: kubectl failed without running the command in
	// the pod (forbidden, pod or container gone, unknown context). It
	// never wraps *exec.ExitError, so no caller mistakes it for the
	// command's own non-zero status.
	ErrUnreachable = fmt.Errorf("k8s: pod not reached: %w", ErrTransport)
)

// Mode is how the tracer gets into the pod (#18).
type Mode string

// Profile is the tracer container's privilege profile (#13).
type Profile string

const (
	// ModeDebug: an ephemeral container, `kubectl debug --target`
	// (the default).
	ModeDebug Mode = "debug"
	// ModeSidecar: a container the deployment owner's sidecar patch
	// added; iormeshctl only execs into it.
	ModeSidecar Mode = "sidecar"
	// ProfileMinimal: #13's capability set (the default).
	ProfileMinimal Profile = "minimal"
	// ProfilePrivileged: `privileged: true`, #13's documented fallback.
	ProfilePrivileged Profile = "privileged"
)

// DefaultSELinuxType is the SELinux type the minimal profile's tracer
// container runs as (decision #13, task 5h4): container_t is denied
// bpf { prog_load } on an SELinux-enforcing node (task zg4), spc_t is
// not, and it keeps #13's capability set, RuntimeDefault seccomp and no
// privilege escalation. Only the tracer container carries it, never the
// pod: the app containers stay container_t. An empty type omits
// seLinuxOptions, leaving the runtime's default.
const DefaultSELinuxType = "spc_t"

// DefaultSELinuxLevel is the MCS level set beside the SELinux type
// unless the owner picks another (task 5h4; -selinux-level and the
// registry's selinux_level since task 8h4). Given a type alone, containerd gives the container a fresh MCS pair
// of its own and relabels the pod's shared files — /etc/hosts,
// resolv.conf, the trace emptyDir — to it, which locks the app
// containers (container_t at the pod's pair) out of them: on el9 the
// gateway's JVM was denied reading /etc/hosts. At s0, which every pair
// dominates, the relabelled files stay readable and writable by the
// app; the price is that they lose the pod's own MCS pair. An owner who
// pins the pod's level (pod securityContext.seLinuxOptions.level) passes
// that same pair instead and keeps MCS isolation of those files; an empty
// level omits the field and gets the type-alone behaviour above.
const DefaultSELinuxLevel = "s0"

// TracerContainer is the tracer container's name in the pod, the
// ephemeral one and the sidecar alike: every exec names it.
const TracerContainer = "iormesh"

// DefaultImage is the built-in tracer image, the last layer of #18's
// precedence. It is empty until the published image exists (task vb4
// wrote scripts/container/tracer-image.Containerfile, task yb4 builds and
// publishes it): until then a deploy without a configured image is
// refused rather than given a placeholder (collect needs no image).
// Setting it also moves deploy/
// off its placeholder (manifest.go, `mage k8sManifests`).
const DefaultImage = ""

// Target is one pod session's resolved kubectl coordinates.
type Target struct {
	Context   string // kubeconfig context, always passed as --context
	Namespace string
	Pod       string
	Container string // the app container: kubectl debug --target
	Image     string // the tracer image, pinned by digest
	Mode      Mode
	Profile   Profile
	// SELinuxType is the minimal profile's seLinuxOptions.type ("" omits
	// the field); the privileged profile ignores it.
	SELinuxType string
	// SELinuxLevel is the seLinuxOptions.level beside a non-empty
	// SELinuxType ("" omits the level; ignored when the type is empty).
	SELinuxLevel string
}

// Layer is one level of #18's precedence: run flags, the pod entry, the
// cluster entry, the k8s: defaults. Empty fields defer to the next
// layer. SELinuxType and SELinuxLevel are pointers because their empty
// value is a choice (omit seLinuxOptions, omit the level), not a
// deferral: nil defers.
type Layer struct {
	Context      string
	Namespace    string
	Image        string
	Mode         Mode
	Profile      Profile
	SELinuxType  *string
	SELinuxLevel *string
}

// Scope is the layers resolved: where pods are looked up and how a
// tracer gets into them, before a pod is picked. A registry pods: entry
// with a label selector resolves to one Scope and then one Target per pod
// the selector matches (Lister, pods.go).
type Scope Layer

// ResolveScope layers ls (highest precedence first: flag, pod entry,
// cluster, k8s: defaults), falls back to DefaultImage, debug mode, the
// minimal profile, DefaultSELinuxType and DefaultSELinuxLevel, and
// validates the result. registered is the registry's list of contexts: one it does not list is refused, as an
// unregistered host is for SSH.
func ResolveScope(registered []string, ls ...Layer) (Scope, error) {
	var s Scope
	for _, l := range slices.Backward(ls) {
		s.Context = pick(l.Context, s.Context)
		s.Namespace = pick(l.Namespace, s.Namespace)
		s.Image = pick(l.Image, s.Image)
		s.Mode = pick(l.Mode, s.Mode)
		s.Profile = pick(l.Profile, s.Profile)
		s.SELinuxType = pickPtr(l.SELinuxType, s.SELinuxType)
		s.SELinuxLevel = pickPtr(l.SELinuxLevel, s.SELinuxLevel)
	}
	s.Image = pick(s.Image, DefaultImage)
	s.Mode = pick(s.Mode, ModeDebug)
	s.Profile = pick(s.Profile, ProfileMinimal)
	s.SELinuxType = pickPtr(s.SELinuxType, new(DefaultSELinuxType))
	s.SELinuxLevel = pickPtr(s.SELinuxLevel, new(DefaultSELinuxLevel))
	if err := s.validate(); err != nil {
		return Scope{}, err
	}
	if !slices.Contains(registered, s.Context) {
		return Scope{}, fmt.Errorf("%w: %q is not a registered context", ErrContext, s.Context)
	}
	return s, nil
}

// Target is the session for pod and its app container in s, validated.
func (s Scope) Target(pod, container string) (Target, error) {
	t := Target{Context: s.Context, Namespace: s.Namespace, Pod: pod, Container: container,
		Image: s.Image, Mode: s.Mode, Profile: s.Profile}
	if s.SELinuxType != nil {
		t.SELinuxType = *s.SELinuxType
	}
	if s.SELinuxLevel != nil {
		t.SELinuxLevel = *s.SELinuxLevel
	}
	if err := t.Validate(); err != nil {
		return Target{}, err
	}
	return t, nil
}

// Resolve is ResolveScope and then the Target for pod and its app
// container: one step for a pods: entry that names its pod.
func Resolve(pod, container string, registered []string, ls ...Layer) (Target, error) {
	s, err := ResolveScope(registered, ls...)
	if err != nil {
		return Target{}, err
	}
	return s.Target(pod, container)
}

// pick returns v unless it is empty, then fallback.
func pick[T ~string](v, fallback T) T {
	if v != "" {
		return v
	}
	return fallback
}

// pickPtr returns v unless it is nil (unset), then fallback: for the
// fields whose empty value is a choice rather than a deferral.
func pickPtr(v, fallback *string) *string {
	if v != nil {
		return v
	}
	return fallback
}

// validate checks the scope's fields. An empty image passes: exec and
// fetch (collect, rm) run in a tracer container already there and need
// none, so a registry without an image — DefaultImage is empty until task
// yb4 publishes one — can still collect; Debug and Deploy refuse it
// (requireImage). A non-empty image must always be pinned by digest.
func (s Scope) validate() error {
	if !k8sname.Context(s.Context) {
		return fmt.Errorf("%w: %q", ErrContext, s.Context)
	}
	if !k8sname.Label(s.Namespace) {
		return fmt.Errorf("%w: namespace %q", ErrName, s.Namespace)
	}
	if s.Image != "" && !k8sname.Pinned(s.Image) {
		return fmt.Errorf("%w: %q is not pinned by digest (<repo>@sha256:<64 hex>)", ErrImage, s.Image)
	}
	if s.Mode != ModeDebug && s.Mode != ModeSidecar {
		return fmt.Errorf("%w: mode %q (debug or sidecar)", ErrTransport, s.Mode)
	}
	if s.Profile != ProfileMinimal && s.Profile != ProfilePrivileged {
		return fmt.Errorf("%w: profile %q (minimal or privileged)", ErrTransport, s.Profile)
	}
	if s.SELinuxType != nil && *s.SELinuxType != "" && !k8sname.SELinuxType(*s.SELinuxType) {
		return fmt.Errorf("%w: SELinux type %q ([a-z0-9_]+, or empty to omit it)", ErrTransport, *s.SELinuxType)
	}
	if s.SELinuxLevel != nil && *s.SELinuxLevel != "" && !k8sname.SELinuxLevel(*s.SELinuxLevel) {
		return fmt.Errorf("%w: SELinux level %q (s0 or s0:cN,cM with N, M <= 1023, or empty to omit it)",
			ErrTransport, *s.SELinuxLevel)
	}
	return nil
}

// Validate checks every field before any reaches a kubectl argv.
func (t Target) Validate() error {
	s := Scope{Context: t.Context, Namespace: t.Namespace, Image: t.Image, Mode: t.Mode, Profile: t.Profile,
		SELinuxType: &t.SELinuxType, SELinuxLevel: &t.SELinuxLevel}
	if err := s.validate(); err != nil {
		return err
	}
	switch {
	case !k8sname.Pod(t.Pod):
		return fmt.Errorf("%w: pod %q", ErrName, t.Pod)
	case !k8sname.Label(t.Container):
		return fmt.Errorf("%w: container %q", ErrName, t.Container)
	case t.Container == TracerContainer:
		return fmt.Errorf("%w: container %q is the tracer's own name", ErrName, t.Container)
	}
	return nil
}

// requireImage refuses a target without a tracer image: what Debug runs
// and what Deploy checks a running tracer container against.
func (t Target) requireImage() error {
	if t.Image == "" {
		return fmt.Errorf("%w: no tracer image configured (set k8s.image or -image, pinned by digest)", ErrImage)
	}
	return nil
}

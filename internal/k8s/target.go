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
}

// Layer is one level of #18's precedence: run flags, the pod entry, the
// cluster entry, the k8s: defaults. Empty fields defer to the next
// layer.
type Layer struct {
	Context   string
	Namespace string
	Image     string
	Mode      Mode
	Profile   Profile
}

// Scope is the layers resolved: where pods are looked up and how a
// tracer gets into them, before a pod is picked. A registry pods: entry
// with a label selector resolves to one Scope and then one Target per pod
// the selector matches (Lister, pods.go).
type Scope Layer

// ResolveScope layers ls (highest precedence first: flag, pod entry,
// cluster, k8s: defaults), falls back to DefaultImage, debug mode and the
// minimal profile, and validates the result. registered is the
// registry's list of contexts: one it does not list is refused, as an
// unregistered host is for SSH.
func ResolveScope(registered []string, ls ...Layer) (Scope, error) {
	var s Scope
	for _, l := range slices.Backward(ls) {
		s.Context = pick(l.Context, s.Context)
		s.Namespace = pick(l.Namespace, s.Namespace)
		s.Image = pick(l.Image, s.Image)
		s.Mode = pick(l.Mode, s.Mode)
		s.Profile = pick(l.Profile, s.Profile)
	}
	s.Image = pick(s.Image, DefaultImage)
	s.Mode = pick(s.Mode, ModeDebug)
	s.Profile = pick(s.Profile, ProfileMinimal)
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
	return nil
}

// Validate checks every field before any reaches a kubectl argv.
func (t Target) Validate() error {
	s := Scope{Context: t.Context, Namespace: t.Namespace, Image: t.Image, Mode: t.Mode, Profile: t.Profile}
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

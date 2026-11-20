package k8s

import (
	"errors"
	"fmt"
	"regexp"
	"slices"

	"iormesh/internal/federate"
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
// publishes it): until then a session without a configured image is
// refused rather than given a placeholder. Setting it also moves deploy/
// off its placeholder (manifest.go, `mage k8sManifests`).
const DefaultImage = ""

// Name patterns, Kubernetes' own: a DNS-1123 label for namespaces and
// containers, a DNS-1123 subdomain for pods.
var (
	labelName     = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	subdomainName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
	// contextName is what kubeconfig context names look like in
	// practice (kind-x, user@cluster, arn:aws:eks:…:cluster/x): no
	// space, quote, control character or leading dash.
	contextName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._:@/+-]{0,252}$`)
	// pinnedImage is a repository (optionally with a registry host,
	// port and tag) followed by a sha256 digest: the digest, not a tag,
	// says which bytes run as root in the pod.
	pinnedImage = regexp.MustCompile(
		`^[a-z0-9][a-z0-9._-]*(?::[0-9]+)?(?:/[a-z0-9][a-z0-9._-]*)*(?::[A-Za-z0-9_][A-Za-z0-9._-]{0,127})?` +
			`@sha256:[0-9a-f]{64}$`)
)

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

// Resolve layers ls (highest precedence first: flag, pod entry,
// cluster, k8s: defaults) into the Target for pod and its app
// container, falls back to DefaultImage, debug mode and the minimal
// profile, and validates the result. registered is the registry's list
// of contexts: one it does not list is refused, as an unregistered host
// is for SSH.
func Resolve(pod, container string, registered []string, ls ...Layer) (Target, error) {
	t := Target{Pod: pod, Container: container}
	for _, l := range slices.Backward(ls) {
		t.Context = pick(l.Context, t.Context)
		t.Namespace = pick(l.Namespace, t.Namespace)
		t.Image = pick(l.Image, t.Image)
		t.Mode = pick(l.Mode, t.Mode)
		t.Profile = pick(l.Profile, t.Profile)
	}
	t.Image = pick(t.Image, DefaultImage)
	t.Mode = pick(t.Mode, ModeDebug)
	t.Profile = pick(t.Profile, ProfileMinimal)
	if err := t.Validate(); err != nil {
		return Target{}, err
	}
	if !slices.Contains(registered, t.Context) {
		return Target{}, fmt.Errorf("%w: %q is not a registered context", ErrContext, t.Context)
	}
	return t, nil
}

// pick returns v unless it is empty, then fallback.
func pick[T ~string](v, fallback T) T {
	if v != "" {
		return v
	}
	return fallback
}

// Validate checks every field before any reaches a kubectl argv.
func (t Target) Validate() error {
	if !contextName.MatchString(t.Context) {
		return fmt.Errorf("%w: %q", ErrContext, t.Context)
	}
	if err := checkNames(t); err != nil {
		return err
	}
	if t.Image == "" {
		return fmt.Errorf("%w: no tracer image configured (set k8s.image or -image, pinned by digest)", ErrImage)
	}
	if !pinnedImage.MatchString(t.Image) {
		return fmt.Errorf("%w: %q is not pinned by digest (<repo>@sha256:<64 hex>)", ErrImage, t.Image)
	}
	if t.Mode != ModeDebug && t.Mode != ModeSidecar {
		return fmt.Errorf("%w: mode %q (debug or sidecar)", ErrTransport, t.Mode)
	}
	if t.Profile != ProfileMinimal && t.Profile != ProfilePrivileged {
		return fmt.Errorf("%w: profile %q (minimal or privileged)", ErrTransport, t.Profile)
	}
	return nil
}

// checkNames checks the namespace, pod and app container names.
func checkNames(t Target) error {
	switch {
	case !labelName.MatchString(t.Namespace):
		return fmt.Errorf("%w: namespace %q", ErrName, t.Namespace)
	case len(t.Pod) > 253 || !subdomainName.MatchString(t.Pod):
		return fmt.Errorf("%w: pod %q", ErrName, t.Pod)
	case !labelName.MatchString(t.Container):
		return fmt.Errorf("%w: container %q", ErrName, t.Container)
	case t.Container == TracerContainer:
		return fmt.Errorf("%w: container %q is the tracer's own name", ErrName, t.Container)
	}
	return nil
}

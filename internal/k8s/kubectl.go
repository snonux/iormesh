package k8s

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"

	"iormesh/internal/federate"
	"iormesh/internal/project"
	"iormesh/internal/segment"
)

// TracesMount is where the size-bounded emptyDir (#13) is mounted in the
// tracer container (docs/implementation.md M7 step 8). The kubelet makes
// an emptyDir's root 0777 (setgid too under fsGroup), which the segment
// writer rightly refuses as a trace dir (segment.prepareDir: another
// writer could swap ready/ for a symlink under root's feet). So the
// tracer writes one level down, in TraceRoot.
const TracesMount = "/opt/iormesh/data"

// TraceRoot is the tracer container's trace dir: a directory the tracer
// itself creates root-owned 0755 inside the volume (segment.MakeDirs), so
// it passes the same trust check as a server's /opt/iormesh/traces. The
// 0777 volume root above it is not checked, and need not be: only the
// tracer container mounts the volume, and no other container of the pod
// can reach it through /proc/<pid>/root without ptrace rights over the
// tracer, which #13's extra capabilities deny a pod's ordinary root.
// `iormesh run` in a pod defaults its -trace-dir to it, `iormesh ready`
// reads it, and collect's remote dir is <root>/<project>/ready.
const TraceRoot = TracesMount + "/traces"

// TracerBinary is the tracer's name inside the image, found on the
// image's PATH (scripts/container/tracer-image.Containerfile sets it;
// task yb4 builds the image).
const TracerBinary = "iormesh"

// removeBatch bounds how many file names one remove exec carries, far
// below any argv limit.
const removeBatch = 256

// readyDirPath is the one remote dir Fetch accepts: a project's ready/
// under TraceRoot. The tracer's `ready` subcommand takes the project, not
// a path, so the controller can never name another directory.
var readyDirPath = regexp.MustCompile(`^` + regexp.QuoteMeta(TraceRoot) + `/([^/]+)/` +
	regexp.QuoteMeta(segment.ReadyDir) + `/?$`)

// Kubectl is the Transport for a tracer in a pod (decisions #12, #18).
// Like federate.SSH it wraps the operator's own binary, so the
// kubeconfig's credentials, exec plugins and proxies just work and
// iormeshctl never holds a token itself — but it always passes the
// Target's context and namespace explicitly, so the kubeconfig's current
// context never picks the cluster.
type Kubectl struct {
	t   Target
	run runner
	// superseded is set by a Deploy that started a debug tracer in
	// place of an ended one (Replaced, task ah4), or left one running
	// that has an ended predecessor (its name past TracerContainer: it
	// may still be attaching): the predecessor's state.json is still in
	// the shared trace dir until the new run attaches, and WaitAttached
	// must not take it for the new one's.
	superseded bool
	// pred is WaitAttached's memory of that state, kept across waits.
	pred *predecessor
}

// Kubectl satisfies federate.Transport; checked at compile time.
var _ federate.Transport = (*Kubectl)(nil)

// New returns the transport for t, validated again here so a Target
// built by hand gets the same checks as a resolved one.
func New(t Target) (*Kubectl, error) {
	return newKubectl(t, execRunner{bin: "kubectl"})
}

// newKubectl is New with the command runner injected.
func newKubectl(t Target, r runner) (*Kubectl, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return &Kubectl{t: t, run: r}, nil
}

// base is every call's leading argv: the context and namespace, in
// --flag=value form so neither can ever read as a separate option.
func (k *Kubectl) base() []string {
	return []string{"--context=" + k.t.Context, "--namespace=" + k.t.Namespace}
}

// tracer is the tracer container this transport execs into: the
// target's Tracer, else TracerContainer.
func (k *Kubectl) tracer() string {
	if k.t.Tracer != "" {
		return k.t.Tracer
	}
	return TracerContainer
}

// Tracer names the tracer container this transport execs into — after
// Deploy, the one it left running or started (task ah4).
func (k *Kubectl) Tracer() string { return k.tracer() }

// execArgs is `kubectl exec` of argv in the tracer container. kubectl
// hands argv to the container runtime as an argv — no shell on either
// side — and without -i nothing reads the operator's terminal.
func (k *Kubectl) execArgs(argv ...string) []string {
	return slices.Concat(k.base(),
		[]string{"exec", k.t.Pod, "--container=" + k.tracer(), "--"}, argv)
}

// Run executes argv in the tracer container. A command that ran and
// exited non-zero returns an error wrapping *exec.ExitError with its
// status, as federate.Transport promises; kubectl's own failures (it
// exits 1 for those too) wrap ErrUnreachable instead and never the
// ExitError — explain (exec.go) tells the two apart by kubectl exec's
// "command terminated with exit code N" line.
func (k *Kubectl) Run(ctx context.Context, argv ...string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("%w: empty remote command", ErrTransport)
	}
	out, err := k.run.output(ctx, k.execArgs(argv...))
	if err != nil {
		return out, fmt.Errorf("kubectl exec %s/%s %s: %w", k.t.Namespace, k.t.Pod, argv[0], err)
	}
	return out, nil
}

// Push is refused: in a pod the tracer comes with the image, pinned by
// digest, and a deploy is a new debug container (Debug), never files
// copied in — #12 rules out `kubectl cp`, which would need tar in the
// image as well.
func (k *Kubectl) Push(context.Context, string, ...string) error {
	return fmt.Errorf("%w: push: the tracer image carries the binary; deploy a pod with a new image instead",
		ErrTransport)
}

// Fetch moves the project's ready/ segments into localDir (decision #12
// as settled by #18): one exec of `iormesh ready -project P` streams them
// as a tar, which Unpack takes apart under collect's rules, and a second
// exec removes, on the tracer, exactly the files that arrived — so a
// failed fetch loses nothing and a repeated one moves only what is new.
// Files already unpacked are removed even when the stream broke later.
// One fetch at a time per local dir (lockDir): Unpack's temporaries are
// per segment name, and two collects of one pod would otherwise write
// the same temporary at once.
func (k *Kubectl) Fetch(ctx context.Context, remoteDir, localDir string) error {
	proj, err := readyProject(remoteDir)
	if err != nil {
		return err
	}
	local, err := filepath.Abs(localDir)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	if err := os.MkdirAll(local, 0o700); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	unlock, err := lockDir(local)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	defer unlock()
	var taken []string
	err = k.run.stream(ctx, k.execArgs(TracerBinary, "ready", "-project", proj), func(r io.Reader) error {
		var uerr error
		taken, uerr = Unpack(r, local)
		return uerr
	})
	// As SSH.Fetch: the host directory is the operator's alone, whatever
	// happened to the stream.
	if cerr := os.Chmod(local, 0o700); cerr != nil && err == nil {
		err = cerr
	}
	if rerr := k.removeTaken(ctx, proj, taken); err == nil {
		err = rerr
	}
	if err != nil {
		return fmt.Errorf("kubectl fetch %s/%s %s: %w", k.t.Namespace, k.t.Pod, proj, err)
	}
	return nil
}

// removeTaken deletes the fetched files on the tracer, in batches.
func (k *Kubectl) removeTaken(ctx context.Context, proj string, taken []string) error {
	for batch := range slices.Chunk(taken, removeBatch) {
		argv := slices.Concat([]string{TracerBinary, "ready", "-project", proj, "-remove", "--"}, batch)
		if _, err := k.run.output(ctx, k.execArgs(argv...)); err != nil {
			return fmt.Errorf("remove fetched segments: %w", err)
		}
	}
	return nil
}

// readyProject maps collect's remote dir to its project, refusing any
// other directory.
func readyProject(remoteDir string) (string, error) {
	m := readyDirPath.FindStringSubmatch(path.Clean(remoteDir) + "/")
	if m == nil || !project.Valid(m[1]) {
		return "", fmt.Errorf("%w: remote dir %q is not %s/<project>/%s", ErrTransport,
			remoteDir, TraceRoot, segment.ReadyDir)
	}
	return m[1], nil
}

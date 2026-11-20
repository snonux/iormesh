package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"slices"
)

// The volumes a prepared pod carries (decision #13, docs/implementation.md
// M7 step 5): an ephemeral container can mount only volumes the pod
// already has, so deploy/prepare-patch.yaml adds them once, by name.
const (
	// TracefsVolume is the host's tracefs, hostPath TracefsPath: libbpf
	// reads each classic tracepoint's id there, and mounting tracefs
	// inside the container would need SYS_ADMIN.
	TracefsVolume = "tracefs"
	TracefsPath   = "/sys/kernel/tracing"
	// TracesVolume is the size-bounded emptyDir mounted at TracesMount,
	// so segments never fill the container's writable layer.
	TracesVolume = "iormesh-traces"
	// PreparePatch names the patch a refusal points the operator at.
	PreparePatch = "deploy/prepare-patch.yaml"
)

// minimalCaps is #13's capability set, everything else dropped:
// tracing (BPF, PERFMON, SYS_RESOURCE for libbpfgo's memlock bump until
// M7 step 2 removes it) and Java attribution (the other four).
var minimalCaps = []string{"BPF", "PERFMON", "SYS_RESOURCE",
	"SYS_PTRACE", "DAC_OVERRIDE", "KILL", "CHECKPOINT_RESTORE"}

// Check reads the pod's spec and refuses a pod no tracer can run in
// (ErrUnprepared): #13's two volumes missing or of the wrong kind, a
// user-namespaced pod (hostUsers: false — its capabilities reach neither
// BPF nor tracing), no such app container to target, for a sidecar no
// tracer container or no shared process namespace, and for a debug
// session a pod that has a tracer already (one tracer per pod).
func (k *Kubectl) Check(ctx context.Context) error {
	p, err := k.getPod(ctx)
	if err != nil {
		return err
	}
	if err := k.checkPod(p); err != nil {
		return err
	}
	return checkContainers(p, k.t)
}

// getPod reads the target pod (`kubectl get pod -o json`).
func (k *Kubectl) getPod(ctx context.Context) (podInfo, error) {
	out, err := k.run.output(ctx, slices.Concat(k.base(), []string{"get", "pod", k.t.Pod, "--output=json"}))
	if err != nil {
		return podInfo{}, fmt.Errorf("kubectl get pod %s/%s: %w", k.t.Namespace, k.t.Pod, err)
	}
	var p podInfo
	if err := json.Unmarshal(out, &p); err != nil {
		return podInfo{}, fmt.Errorf("kubectl get pod %s/%s: %w", k.t.Namespace, k.t.Pod, err)
	}
	return p, nil
}

// checkPod refuses a pod without #13's volumes, naming the prepare patch,
// or without the app container to target: what every mode needs. It
// also refuses a pod whose identity a tracer in it would refuse
// (PodRef.Validate, task 7f4: a static pod's UID, an FQDN over
// MaxFQDN), so iormeshctl says so up front instead of starting a tracer
// that exits with ErrPodEnv.
func (k *Kubectl) checkPod(p podInfo) error {
	if err := checkPrepared(p); err != nil {
		return fmt.Errorf("%w: %s/%s: %s; apply %s to its workload first", ErrUnprepared,
			k.t.Namespace, k.t.Pod, err, PreparePatch)
	}
	if !hasContainer(p.Spec.Containers, k.t.Container) {
		return fmt.Errorf("%w: %s/%s has no container %q", ErrUnprepared, k.t.Namespace, k.t.Pod, k.t.Container)
	}
	id := PodRef{Name: p.Metadata.Name, Namespace: k.t.Namespace, UID: p.Metadata.UID}
	if err := id.Validate(); err != nil {
		return fmt.Errorf("%w: %s/%s: a tracer in this pod would refuse to start: %w", ErrName,
			k.t.Namespace, k.t.Pod, err)
	}
	return nil
}

// checkPrepared checks the pod-level requirements, the ones the prepare
// patch provides.
func checkPrepared(p podInfo) error {
	if p.Spec.HostUsers != nil && !*p.Spec.HostUsers {
		return fmt.Errorf("hostUsers is false (a user-namespaced pod's capabilities do not reach BPF)")
	}
	var tracefs, traces bool
	for _, v := range p.Spec.Volumes {
		switch v.Name {
		case TracefsVolume:
			tracefs = v.HostPath != nil && path.Clean(v.HostPath.Path) == TracefsPath
		case TracesVolume:
			traces = v.EmptyDir != nil && v.EmptyDir.SizeLimit != ""
		}
	}
	if !tracefs {
		return fmt.Errorf("no volume %q (hostPath %s)", TracefsVolume, TracefsPath)
	}
	if !traces {
		return fmt.Errorf("no volume %q (emptyDir with a sizeLimit)", TracesVolume)
	}
	return nil
}

// checkContainers checks the tracer container against the mode (the app
// container is checkPod's): a sidecar must be there, with the shared
// process namespace it traces through; a debug target must have no
// tracer yet, neither a debug container nor a sidecar.
func checkContainers(p podInfo, t Target) error {
	if t.Mode == ModeSidecar {
		return checkSidecar(p, t)
	}
	// One tracer per pod (PLAN.md M7): a tracer's host_id is the pod
	// (pod.go HostID) and its pids are the pod's PID namespace's, so a
	// second tracer in the same pod would write a second session under
	// the same identity. Both refusals below keep it at one.
	if hasContainer(p.Spec.EphemeralContainers, TracerContainer) {
		// Ephemeral containers can be neither removed nor restarted:
		// a second debug of the same name would fail in the API server.
		return fmt.Errorf("%w: %s/%s already has a %q debug container; restart the pod for a new session",
			ErrTransport, t.Namespace, t.Pod, TracerContainer)
	}
	if hasContainer(p.Spec.Containers, TracerContainer) {
		// A sidecar-patched pod has its tracer already: use sidecar mode.
		return fmt.Errorf("%w: %s/%s already has a %q sidecar; trace it in sidecar mode",
			ErrTransport, t.Namespace, t.Pod, TracerContainer)
	}
	return nil
}

// checkSidecar refuses a sidecar-mode pod without the tracer container or
// without shareProcessNamespace, naming the sidecar patch.
func checkSidecar(p podInfo, t Target) error {
	if !hasContainer(p.Spec.Containers, TracerContainer) ||
		p.Spec.ShareProcessNamespace == nil || !*p.Spec.ShareProcessNamespace {
		return fmt.Errorf("%w: %s/%s: no %q sidecar with shareProcessNamespace; apply the sidecar patch",
			ErrUnprepared, t.Namespace, t.Pod, TracerContainer)
	}
	return nil
}

// Debug starts the tracer in the pod as an ephemeral container running
// argv (decision #14: `kubectl debug --target` joins the app container's
// process namespace, so the JVM is never restarted). It runs Check first
// and refuses a sidecar-mode target, which has its tracer already, and a
// target without an image. Deploy (deploy.go) is what iormeshctl calls:
// it also accepts a tracer already running the pinned image.
func (k *Kubectl) Debug(ctx context.Context, argv ...string) error {
	if k.t.Mode != ModeDebug {
		return fmt.Errorf("%w: debug in %s mode", ErrTransport, k.t.Mode)
	}
	if len(argv) == 0 {
		return fmt.Errorf("%w: empty tracer command", ErrTransport)
	}
	if err := k.t.requireImage(); err != nil {
		return err
	}
	if err := k.Check(ctx); err != nil {
		return err
	}
	return k.debug(ctx, argv)
}

// debug runs `kubectl debug` once the pod has been checked.
func (k *Kubectl) debug(ctx context.Context, argv []string) error {
	custom, err := writeCustom(k.t.Profile)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(custom) }()
	if _, err := k.run.output(ctx, k.debugArgs(custom, argv)); err != nil {
		return fmt.Errorf("kubectl debug %s/%s: %w", k.t.Namespace, k.t.Pod, err)
	}
	return nil
}

// debugArgs is the `kubectl debug` argv. --attach=false: the tracer runs
// on in the pod, iormeshctl does not hold a terminal to it. The profile
// is general for the minimal set (the --custom file then sets the whole
// security context) and sysadmin (privileged) for the fallback.
func (k *Kubectl) debugArgs(custom string, argv []string) []string {
	profile := "general"
	if k.t.Profile == ProfilePrivileged {
		profile = "sysadmin"
	}
	return slices.Concat(k.base(), []string{"debug", k.t.Pod,
		"--container=" + TracerContainer,
		"--target=" + k.t.Container,
		"--image=" + k.t.Image,
		"--attach=false",
		"--profile=" + profile,
		"--custom=" + custom,
		"--"}, argv)
}

// customSpec is the --custom partial container spec
// (docs/implementation.md M7 step 8): #13's security context, the two
// volume mounts, and the pod's identity from the downward API
// (PodFromEnv: how an in-pod tracer under a private cgroup namespace
// knows its pod, and what its project defaults to). The sidecar patch
// (manifest_spec.go) is this spec too, for the minimal profile.
func customSpec(p Profile) map[string]any {
	sc := map[string]any{"runAsUser": 0}
	if p == ProfileMinimal {
		// privileged must not set allowPrivilegeEscalation: false (the
		// API refuses the pair), and sysadmin already sets the rest.
		sc["allowPrivilegeEscalation"] = false
		sc["seccompProfile"] = map[string]any{"type": "RuntimeDefault"}
		sc["capabilities"] = map[string]any{"drop": []string{"ALL"}, "add": minimalCaps}
	}
	return map[string]any{
		"env":             downwardEnv(),
		"securityContext": sc,
		"volumeMounts": []map[string]any{
			{"name": TracefsVolume, "mountPath": TracefsPath, "readOnly": true},
			{"name": TracesVolume, "mountPath": TracesMount},
		},
	}
}

// writeCustom writes customSpec to a private temporary file (0600, from
// os.CreateTemp) and returns its path; the caller removes it.
func writeCustom(p Profile) (string, error) {
	b, err := json.Marshal(customSpec(p))
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "iormesh-debug-*.json")
	if err != nil {
		return "", err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

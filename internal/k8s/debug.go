package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
	// TracesVolume is the size-bounded emptyDir mounted at TraceRoot,
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

// podInfo is the part of `kubectl get pod -o json` the checks read.
type podInfo struct {
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
}

// named is a container entry; only its name matters here.
type named struct {
	Name string `json:"name"`
}

// Check reads the pod's spec and refuses a pod no tracer can run in
// (ErrUnprepared): #13's two volumes missing or of the wrong kind, a
// user-namespaced pod (hostUsers: false — its capabilities reach neither
// BPF nor tracing), an unknown app container, or — for a sidecar — no
// tracer container or no shared process namespace.
func (k *Kubectl) Check(ctx context.Context) error {
	out, err := k.run.output(ctx, slices.Concat(k.base(), []string{"get", "pod", k.t.Pod, "--output=json"}))
	if err != nil {
		return fmt.Errorf("kubectl get pod %s/%s: %w", k.t.Namespace, k.t.Pod, err)
	}
	var p podInfo
	if err := json.Unmarshal(out, &p); err != nil {
		return fmt.Errorf("kubectl get pod %s/%s: %w", k.t.Namespace, k.t.Pod, err)
	}
	if err := checkPrepared(p); err != nil {
		return fmt.Errorf("%w: %s/%s: %s; apply %s to its workload first", ErrUnprepared,
			k.t.Namespace, k.t.Pod, err, PreparePatch)
	}
	return checkContainers(p, k.t)
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
			tracefs = v.HostPath != nil && v.HostPath.Path == TracefsPath
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

// checkContainers checks the app container and the tracer container
// against the mode.
func checkContainers(p podInfo, t Target) error {
	has := func(cs []named, name string) bool {
		return slices.ContainsFunc(cs, func(c named) bool { return c.Name == name })
	}
	if !has(p.Spec.Containers, t.Container) {
		return fmt.Errorf("%w: %s/%s has no container %q", ErrName, t.Namespace, t.Pod, t.Container)
	}
	if t.Mode == ModeSidecar {
		if !has(p.Spec.Containers, TracerContainer) ||
			p.Spec.ShareProcessNamespace == nil || !*p.Spec.ShareProcessNamespace {
			return fmt.Errorf("%w: %s/%s: no %q sidecar with shareProcessNamespace; apply the sidecar patch",
				ErrUnprepared, t.Namespace, t.Pod, TracerContainer)
		}
		return nil
	}
	if has(p.Spec.EphemeralContainers, TracerContainer) {
		// Ephemeral containers can be neither removed nor restarted:
		// a second debug of the same name would fail in the API server.
		return fmt.Errorf("%w: %s/%s already has a %q debug container; restart the pod for a new session",
			ErrTransport, t.Namespace, t.Pod, TracerContainer)
	}
	return nil
}

// Debug starts the tracer in the pod as an ephemeral container running
// argv (decision #14: `kubectl debug --target` joins the app container's
// process namespace, so the JVM is never restarted). It runs Check first
// and refuses a sidecar-mode target, which has its tracer already.
func (k *Kubectl) Debug(ctx context.Context, argv ...string) error {
	if k.t.Mode != ModeDebug {
		return fmt.Errorf("%w: debug in %s mode", ErrTransport, k.t.Mode)
	}
	if len(argv) == 0 {
		return fmt.Errorf("%w: empty tracer command", ErrTransport)
	}
	if err := k.Check(ctx); err != nil {
		return err
	}
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
// (docs/implementation.md M7 step 8): #13's security context and the two
// volume mounts.
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
		"securityContext": sc,
		"volumeMounts": []map[string]any{
			{"name": TracefsVolume, "mountPath": TracefsPath, "readOnly": true},
			{"name": TracesVolume, "mountPath": TraceRoot},
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

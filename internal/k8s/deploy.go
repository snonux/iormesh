package k8s

import (
	"context"
	"fmt"
	"slices"

	"iormesh/internal/k8sname"
)

// Outcome is what Deploy did in a pod.
type Outcome int

const (
	// Started: a new ephemeral tracer container runs argv.
	Started Outcome = iota
	// Running: the pod's ephemeral tracer container from an earlier
	// deploy is running the pinned image; it was left alone (ephemeral
	// containers can be neither restarted nor removed).
	Running
	// SidecarVerified: sidecar mode; the deployment owner's sidecar is
	// there and running the pinned image. iormeshctl starts nothing in
	// a sidecar pod: the patch fixed its command line.
	SidecarVerified
)

// Deploy is iormeshctl deploy's step for one pod (decision #18, task
// af4). It reads the pod once and refuses one without #13's volumes or
// the app container (ErrUnprepared), then by mode:
//
//   - sidecar: the "iormesh" container and shareProcessNamespace must be
//     there, and the container must be running the pinned image;
//   - debug, with an "iormesh" ephemeral container already (an earlier
//     deploy): it must be running the pinned image, and is left alone —
//     one that ended, or runs another image, is refused (restart the pod);
//   - debug otherwise: `kubectl debug` starts the tracer running argv.
//
// "Running the pinned image" is checked against the container status's
// imageID, the digest the kubelet actually pulled, never the spec's
// image field: a sidecar patch may name a tag, and only the digest says
// which bytes run with #13's capabilities (verifyImage). So the
// digest-pinning rule holds in sidecar mode too rather than being
// dropped — the owner's patch need not pin, the check is iormeshctl's.
func (k *Kubectl) Deploy(ctx context.Context, argv ...string) (Outcome, error) {
	if err := k.t.requireImage(); err != nil {
		return 0, err
	}
	p, err := k.getPod(ctx)
	if err != nil {
		return 0, err
	}
	if err := k.checkPod(p); err != nil {
		return 0, err
	}
	if k.t.Mode == ModeSidecar {
		if err := checkSidecar(p, k.t); err != nil {
			return 0, err
		}
		if err := k.verifyImage(p.Status.ContainerStatuses); err != nil {
			return 0, err
		}
		return SidecarVerified, nil
	}
	if hasContainer(p.Spec.Containers, TracerContainer) {
		// One tracer per pod (PLAN.md M7, task 7f4): a sidecar pod has
		// its tracer already, and a debug one beside it would be a
		// second session under the same pod identity.
		return 0, fmt.Errorf("%w: %s/%s already has a %q sidecar; trace it in sidecar mode",
			ErrTransport, k.t.Namespace, k.t.Pod, TracerContainer)
	}
	if hasContainer(p.Spec.EphemeralContainers, TracerContainer) {
		if err := k.verifySpecImage(p.Spec.EphemeralContainers); err != nil {
			return 0, err
		}
		if err := k.verifyImage(p.Status.EphemeralContainerStatuses); err != nil {
			return 0, fmt.Errorf("%w; ephemeral containers cannot be replaced: restart the pod for a new session",
				err)
		}
		return Running, nil
	}
	if len(argv) == 0 {
		return 0, fmt.Errorf("%w: empty tracer command", ErrTransport)
	}
	if err := k.debug(ctx, argv); err != nil {
		return 0, err
	}
	return Started, nil
}

// verifyImage finds the tracer container's status in statuses and
// refuses it unless it is running the target's pinned digest. An imageID
// that names no repository digest (a bare "sha256:<config digest>", as a
// locally loaded image reports) cannot be checked and is refused too:
// unverifiable is not verified.
func (k *Kubectl) verifyImage(statuses []containerStatus) error {
	where := k.tracerName()
	st, ok := tracerStatus(statuses)
	if !ok {
		return fmt.Errorf("%w: %s has no status yet (not started)", ErrTransport, where)
	}
	if why := st.notRunning(); why != "" {
		return fmt.Errorf("%w: %s is %s", ErrTransport, where, why)
	}
	got, want := k8sname.StatusDigest(st.ImageID), k8sname.Digest(k.t.Image)
	switch {
	case got == "":
		return fmt.Errorf("%w: %s runs imageID %q, which names no registry digest to check against %s",
			ErrImage, where, st.ImageID, k.t.Image)
	case got != want:
		return fmt.Errorf("%w: %s runs sha256:%s, not the pinned %s", ErrImage, where, got, k.t.Image)
	}
	return nil
}

// verifySpecImage requires an earlier debug container's spec image to be
// pinned by the target's digest too (review N6): iormeshctl always
// creates it with --image=<digest>, so anything else was made by someone
// else, and the status alone is written by the node, not the API's
// admission. A sidecar's spec may name a tag (the owner's patch); only
// its status is checked.
func (k *Kubectl) verifySpecImage(cs []named) error {
	i := slices.IndexFunc(cs, func(c named) bool { return c.Name == TracerContainer })
	if i < 0 || k8sname.Digest(cs[i].Image) != k8sname.Digest(k.t.Image) {
		var img string
		if i >= 0 {
			img = cs[i].Image
		}
		return fmt.Errorf("%w: %s has spec image %q, not the pinned %s; restart the pod for a new session",
			ErrImage, k.tracerName(), img, k.t.Image)
	}
	return nil
}

// tracerName names the target's tracer container in messages.
func (k *Kubectl) tracerName() string {
	return fmt.Sprintf("%s/%s container %q", k.t.Namespace, k.t.Pod, TracerContainer)
}

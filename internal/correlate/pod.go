package correlate

import "iormesh/internal/schema"

// The pod object of an in-pod tracer's events (PLAN.md M7 "Pod fields on
// events", task sh4). The pod is the tracer's own, from the downward API;
// the container is the event's process's, from its cgroup. In sidecar
// mode with a shared PID namespace one tracer sees the app's, the
// sidecars' and its own processes, and the container tells them apart.

// PodConfig is the pod an in-pod tracer runs in (k8s.PodRef's namespace
// and name; its UID is already the host id).
type PodConfig struct {
	Namespace, Name string
}

// podOf is the pod object of an event of pid's incarnation inc: the pod
// alone for a host-level event (pid 0) and for a process the correlator
// no longer holds as that incarnation (replaced by a later process under
// the same pid), else the process's own (procPod). Called at enqueue,
// so the process state is the one the event was built from. The objects
// are shared and never changed after they are made: the sink borrows an
// event for its call only and copies what it keeps.
func (c *Correlator) podOf(pid uint32, inc uint64) *schema.Pod {
	if pid == 0 {
		return c.hostPod
	}
	p := c.lastProc
	if p == nil || c.lastPID != pid {
		p = c.procs[pid]
	}
	if p == nil || p.inc != inc {
		return c.hostPod
	}
	return c.procPod(p)
}

// procPod is p's pod object, asking ContainerOf on first use: once per
// process incarnation, never per event. A process in no container of
// the pod (the tracer's own, the pod cgroup, a vanished process) shares
// hostPod.
func (c *Correlator) procPod(p *procState) *schema.Pod {
	if p.pod != nil {
		return p.pod
	}
	ctr := ""
	if c.cfg.ContainerOf != nil {
		ctr = c.cfg.ContainerOf(p.pid)
	}
	p.pod = c.containerPod(ctr)
	return p.pod
}

// containerPod is the shared pod object of container ctr ("" for none).
func (c *Correlator) containerPod(ctr string) *schema.Pod {
	if ctr == "" {
		return c.hostPod
	}
	pod, ok := c.pods[ctr]
	if !ok {
		pod = &schema.Pod{Namespace: c.hostPod.Namespace, Name: c.hostPod.Name, ContainerID: ctr}
		c.pods[ctr] = pod
	}
	return pod
}

// inheritPod gives a forked child its parent's container without a
// cgroup read: the kernel places the child in its parent's cgroup at
// fork (cgroup_post_fork), and the child may be gone, or its pid reused,
// by the time the correlator would read its /proc entry. A clone3 with
// CLONE_INTO_CGROUP places it elsewhere; no container runtime's traced
// process does that, and the child then carries its parent's container.
func (c *Correlator) inheritPod(child, parent *procState) {
	if c.hostPod != nil {
		child.pod = c.procPod(parent)
	}
}

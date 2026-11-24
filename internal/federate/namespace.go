package federate

import (
	"fmt"

	"iormesh/internal/k8sname"
	"iormesh/internal/schema"
)

// Kubernetes namespaces of the store's sessions (docs/milestones.md M7 "Pod
// fields on events", task sh4). An in-pod tracer's session is a pod's: host id
// "pod-<uid>", FQDN "<name>.<namespace>.<uid[:8]>", which collect pins
// to the pod's API metadata and vetHosts holds every session to. The
// namespace is read from that identity, never from the events alone, so
// segments written before the events carried a pod object count too.

// HostID is the host id the segment's events carry (the check's).
func (s *Segment) HostID() string { return s.hostID }

// Namespace is the Kubernetes namespace of the segment's pod, "" for a
// session that is no pod's (a machine-id host).
func (s *Segment) Namespace() string {
	ns, _ := k8sname.PodNamespace(s.hostID, s.File.FQDN)
	return ns
}

// InNamespace returns the segments of pods in namespace ns: Sources and
// TableFiles of the sessions whose host identity names ns (a host outside
// a pod names none and is left out), Hosts narrowed to their
// directories. Bad and Ignored stay whole — a damaged segment cannot say
// whose it was, and hiding it would make the loss silent.
func (o Segments) InNamespace(ns string) Segments {
	out := o
	out.Sources, out.TableFiles, out.Hosts = nil, nil, map[string]HostIdentity{}
	sessions := map[string]bool{}
	for _, s := range o.Sources {
		if s.Namespace() == ns {
			out.Sources = append(out.Sources, s)
			sessions[s.Session()] = true
			if id, ok := o.Hosts[s.Host]; ok {
				out.Hosts[s.Host] = id
			}
		}
	}
	for _, t := range o.TableFiles {
		if sessions[t.Session()] {
			out.TableFiles = append(out.TableFiles, t)
		}
	}
	return out
}

// checkPod holds an event's pod object to its session's identity: the
// pod name and namespace the tracer stamps must be the ones its pinned
// FQDN names, or a tracer could place its events in another namespace's
// filtered build. An event without a pod object (outside a pod, or
// written before task sh4) passes; schema.Event.Validate has already
// required a pod's host id for one that has it.
func checkPod(ev *schema.Event, fqdn string) error {
	p := ev.Pod
	if p == nil {
		return nil
	}
	uid := ev.HostID[len(k8sname.PodHostPrefix):]
	if want := p.Name + "." + p.Namespace + "." + uid[:min(8, len(uid))]; want != fqdn {
		return fmt.Errorf("event seq %d: pod %s/%s, but the session is %s", ev.Seq, p.Namespace, p.Name, fqdn)
	}
	return nil
}

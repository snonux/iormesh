package expand

import (
	"net/netip"
	"slices"
	"strings"

	"iormesh/internal/federate"
	"iormesh/internal/schema"
)

// evidence is what the merge and the registry prove about addresses and
// hosts, gathered once per plan.
type evidence struct {
	// dirs maps a trace's host_id to its store directory, which is the
	// registry host name collect fetched it from.
	dirs map[string]string
	// hops maps a registered host to its hop; registered hosts only.
	hops map[string]int
	// literal maps an address a registry host is written as (user@
	// stripped) to that host.
	literal map[netip.Addr]string
	// owned maps an address a traced host used as its own local end to
	// that host (its directory, or its host_id when it has none).
	owned map[netip.Addr]string
	// nat maps an address a fused connection proved to translate to a
	// traced server ("client reached X, server listened on Y≠X") to that
	// server's host.
	nat map[netip.Addr]string
	// apps labels process node ids by their app, else their comm.
	apps map[string]string
	// comms maps process node ids to their comm.
	comms map[string]string
}

// The controller's own traffic (docs/milestones.md M6: "the controller's
// SSH/rsync traffic is excluded"). Collect, deploy and rm reach every host
// over SSH, rsync riding on it. On a traced host that traffic is inbound to
// sshd, never a candidate; it can only propose a peer when a traced
// process is the SSH client itself — a project whose pattern matches
// ssh or rsync, or the controller's own machine registered and traced —
// and the peer is then an sshd. Binding a tracer to sshd would trace the
// controller's own sessions, and the edge is the tool's, not the mesh's,
// so neither the SSH port nor a transport tool's connection proposes a
// peer. A service that really speaks SSH is added by hand.
const sshPort = 22

// transportComms are the controller's transport tools as the kernel
// names their processes (sshd's per-session children included).
var transportComms = map[string]bool{"ssh": true, "sshd": true, "sshd-session": true,
	"rsync": true, "scp": true, "sftp-server": true}

// transportProc is the comm of the first of procs that is one of the
// controller's transport tools, or "".
func (ev *evidence) transportProc(procs []string) string {
	for _, p := range procs {
		if c := ev.comms[p]; transportComms[c] {
			return c
		}
	}
	return ""
}

// gather reads in into an evidence.
func gather(in Input) evidence {
	ev := evidence{dirs: in.HostDirs, hops: map[string]int{}, literal: map[netip.Addr]string{},
		owned: map[netip.Addr]string{}, nat: map[netip.Addr]string{}, apps: appLabels(in.Merge.Graph),
		comms: processComms(in.Merge.Graph)}
	for _, s := range in.Registry.Servers {
		ev.hops[s.Host] = s.Hop
		if a, ok := registryAddr(s.Host); ok {
			ev.literal[a] = s.Host
		}
	}
	for _, c := range in.Merge.Conns {
		ev.own(c.Client)
		ev.own(c.Server)
		front, back := c.Client.Peer.Addr().Unmap(), c.Server.Local.Addr().Unmap()
		if routable(front) && routable(back) && front != back {
			keepSmallest(ev.nat, front, ev.host(c.Server.Host))
		}
	}
	for _, u := range in.Merge.Unresolved {
		ev.own(u.Half)
	}
	// An address a traced host owns is that host, NAT or not: owned
	// wins, so the nat set holds only addresses no traced host claims.
	for a := range ev.owned {
		delete(ev.nat, a)
	}
	return ev
}

// own records h's local address as its host's. Two private networks may
// reuse an address, so this can wrongly call a stranger enrolled — a
// peer skipped, never one touched, which is the safe side to err on.
func (ev *evidence) own(h federate.Half) {
	if a := h.Local.Addr().Unmap(); routable(a) {
		keepSmallest(ev.owned, a, ev.host(h.Host))
	}
}

// keepSmallest sets m[a] = host unless a smaller host is there already:
// when two hosts claim one address, the answer must not depend on the
// order the merge listed them in.
func keepSmallest(m map[netip.Addr]string, a netip.Addr, host string) {
	if old, ok := m[a]; !ok || host < old {
		m[a] = host
	}
}

// registryAddr is the address a registry host is written as, user@
// dropped; false for a name. Names are not resolved: what a name points
// at from the controller is not proof of what the traces reached.
func registryAddr(host string) (netip.Addr, bool) {
	if i := strings.LastIndexByte(host, '@'); i >= 0 {
		host = host[i+1:]
	}
	a, err := netip.ParseAddr(host)
	return a.Unmap(), err == nil
}

// host names a trace's host: its store directory, else its host_id.
func (ev *evidence) host(hostID string) string {
	if d, ok := ev.dirs[hostID]; ok {
		return d
	}
	return hostID
}

// app labels a half's processes: their apps' (or comms') sorted labels.
func (ev *evidence) app(procs []string) string {
	var labels []string
	for _, p := range procs {
		if l := ev.apps[p]; l != "" {
			labels = append(labels, l)
		}
	}
	slices.Sort(labels)
	return strings.Join(slices.Compact(labels), ",")
}

// appLabels maps every process node to its app node id (via the
// incarnation edges), or to its comm when no app owns it (a -pid or
// -listen-port session).
func appLabels(g schema.Graph) map[string]string {
	out := processComms(g)
	for _, e := range g.Edges {
		if e.Type == schema.EdgeIncarnation {
			out[e.To] = e.From
		}
	}
	return out
}

// processComms maps every process node to its comm.
func processComms(g schema.Graph) map[string]string {
	out := map[string]string{}
	for _, n := range g.Nodes {
		if n.Type == schema.NodeProcess {
			out[n.ID] = n.Attrs["comm"]
		}
	}
	return out
}

// routable reports whether a is an address another host could be reached
// at: not invalid, unspecified, loopback, link-local or multicast. A peer
// that is not names this host (loopback), a link no controller route
// leads to, or no host at all.
func routable(a netip.Addr) bool {
	return a.IsValid() && !a.IsUnspecified() && !a.IsLoopback() &&
		!a.IsLinkLocalUnicast() && !a.IsLinkLocalMulticast() && !a.IsMulticast() &&
		!a.IsInterfaceLocalMulticast()
}

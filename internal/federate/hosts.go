package federate

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// A server picks its own segment names and writes its own events, and a
// compromised one could name its files after another host: hostB's FQDN
// in the name and footer, hostB's host_id in every event, dropped into
// its own ready/. collect gives every registry host its own store
// directory, so the directory is the one thing the server cannot choose;
// vetHosts ties each host identity to exactly one directory. Without it
// the merge, the service database (keyed by host id, and additive, so a
// planted row stays) and the summary would take the forgery as hostB's.

// HostIdentity is who a host directory's sessions say they are: the
// host_id (machine id) of their events and the FQDN of their names.
type HostIdentity struct {
	HostID string `json:"host_id"`
	FQDN   string `json:"fqdn"`
}

// vetHosts drops the sessions whose host identity is in doubt and
// returns the rest; out.Bad names every dropped segment and out.Hosts
// gets each directory's identity. A session is dropped when
//
//   - a segment's host id differs from the session's first segment's
//     (that segment only: a file planted into a run);
//   - its directory is pinned (known) to another identity (checked
//     first, so a forgery in a pinned directory taints nothing else);
//   - its directory holds sessions of two identities (all of them: which
//     one is the forgery cannot be told);
//   - its host id or its FQDN also appears under another directory (all
//     sessions involved, for the same reason).
func vetHosts(groups [][]*Segment, known map[string]HostIdentity, out *Segments) [][]*Segment {
	groups = pinnedOnly(sessionHostIDs(groups, out), known, out)
	ids := map[HostIdentity]map[string]bool{}
	for _, g := range groups {
		addSet(ids, identityOf(g), g[0].Host)
	}
	doubt := doubtfulDirs(ids)
	out.Hosts = map[string]HostIdentity{}
	var keep [][]*Segment
	for _, g := range groups {
		id, dir := identityOf(g), g[0].Host
		if why := doubt(id, dir); why != "" {
			for _, s := range g {
				out.bad(s, fmt.Errorf("%s: %s", s.Name(), why))
			}
			continue
		}
		out.Hosts[dir] = id
		keep = append(keep, g)
	}
	return keep
}

// sessionHostIDs drops each segment whose host id is not its session's
// (the session's first segment's).
func sessionHostIDs(groups [][]*Segment, out *Segments) [][]*Segment {
	var keep [][]*Segment
	for _, g := range groups {
		want := g[0].hostID
		var ok []*Segment
		for _, s := range g {
			if s.hostID != want {
				out.bad(s, fmt.Errorf("%s: host_id %q, the session's is %q", s.Name(), s.hostID, want))
				continue
			}
			ok = append(ok, s)
		}
		keep = append(keep, ok)
	}
	return keep
}

// pinnedOnly drops the sessions whose directory is pinned to another
// identity. It runs before the cross-directory checks, so a forgery in
// a pinned directory cannot taint the genuine host it imitates.
func pinnedOnly(groups [][]*Segment, known map[string]HostIdentity, out *Segments) [][]*Segment {
	var keep [][]*Segment
	for _, g := range groups {
		id, dir := identityOf(g), g[0].Host
		if pin, pinned := known[dir]; pinned && pin != id {
			for _, s := range g {
				out.bad(s, fmt.Errorf("%s: host_id %s / %s, but %s is pinned to host_id %s / %s",
					s.Name(), id.HostID, id.FQDN, dir, pin.HostID, pin.FQDN))
			}
			continue
		}
		keep = append(keep, g)
	}
	return keep
}

func identityOf(g []*Segment) HostIdentity {
	return HostIdentity{HostID: g[0].hostID, FQDN: g[0].File.FQDN}
}

// doubtfulDirs returns the test vetHosts applies to one session: why
// its identity id in directory dir is in doubt, "" when it is not.
func doubtfulDirs(ids map[HostIdentity]map[string]bool) func(HostIdentity, string) string {
	perDir := map[string]map[HostIdentity]bool{}
	byHostID, byFQDN := map[string]map[string]bool{}, map[string]map[string]bool{}
	for id, dirs := range ids {
		for d := range dirs {
			addSet(perDir, d, id)
			addSet(byHostID, id.HostID, d)
			addSet(byFQDN, id.FQDN, d)
		}
	}
	return func(id HostIdentity, dir string) string {
		switch {
		case len(perDir[dir]) > 1:
			return fmt.Sprintf("%s holds sessions of %d different hosts", dir, len(perDir[dir]))
		case len(byHostID[id.HostID]) > 1:
			return fmt.Sprintf("host_id %s appears under %s", id.HostID, dirList(byHostID[id.HostID]))
		case len(byFQDN[id.FQDN]) > 1:
			return fmt.Sprintf("FQDN %s appears under %s", id.FQDN, dirList(byFQDN[id.FQDN]))
		}
		return ""
	}
}

func dirList(set map[string]bool) string {
	return strings.Join(slices.Sorted(maps.Keys(set)), " and ")
}

func addSet[K, V comparable](m map[K]map[V]bool, k K, v V) {
	if m[k] == nil {
		m[k] = map[V]bool{}
	}
	m[k][v] = true
}

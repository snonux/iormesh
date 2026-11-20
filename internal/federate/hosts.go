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
//   - its directory is pinned (known) to another identity, or its host id
//     or FQDN is pinned to another directory (checked first: only the
//     claimant is dropped, the pinned host's sessions stay good);
//   - its directory holds sessions of two identities (all of them: which
//     one is the forgery cannot be told) — among the unpinned only;
//   - its host id or its FQDN also appears under another directory (all
//     sessions involved, for the same reason) — among the unpinned only.
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

// pinnedOnly drops the sessions the pins rule out: one in a directory
// pinned to another identity, and one anywhere else claiming a host id
// or FQDN pinned to another directory. It runs before the
// cross-directory checks, so a forgery — in the imitated host's own
// directory or in a new one — never reaches them, and the pinned
// host's genuine sessions stay good.
func pinnedOnly(groups [][]*Segment, known map[string]HostIdentity, out *Segments) [][]*Segment {
	idDir, fqdnDir := map[string]string{}, map[string]string{}
	for dir, pin := range known {
		idDir[pin.HostID], fqdnDir[pin.FQDN] = dir, dir
	}
	var keep [][]*Segment
	for _, g := range groups {
		id, dir := identityOf(g), g[0].Host
		why := ""
		if pin, pinned := known[dir]; pinned && pin != id {
			why = fmt.Sprintf("host_id %s / %s, but %s is pinned to host_id %s / %s",
				id.HostID, id.FQDN, dir, pin.HostID, pin.FQDN)
		} else if d, ok := idDir[id.HostID]; ok && d != dir {
			why = fmt.Sprintf("host_id %s is pinned to %s, not %s", id.HostID, d, dir)
		} else if d, ok := fqdnDir[id.FQDN]; ok && d != dir {
			why = fmt.Sprintf("FQDN %s is pinned to %s, not %s", id.FQDN, d, dir)
		}
		if why != "" {
			for _, s := range g {
				out.bad(s, fmt.Errorf("%s: %s", s.Name(), why))
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

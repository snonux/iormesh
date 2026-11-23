package servicedb

import (
	"cmp"
	"context"
	"net/netip"
	"slices"

	"iormesh/internal/federate"
	"iormesh/internal/pathtmpl"
	"iormesh/internal/schema"
)

// Roles of an observation's anchor end. RoleUnknown is a half no record
// gave a role (the merge's no_role): its far end may be a client's
// ephemeral port as well as a service, so it names no endpoint.
const (
	RoleClient  = federate.RoleClient
	RoleServer  = federate.RoleServer
	RoleUnknown = "unknown"
)

// Snapshot is what one build observed: every TCP connection of the
// merged mesh, sorted by anchor (Host, ConnID). Observe produces it and
// DB.Import folds it in; it is plain data so the two are tested apart.
type Snapshot struct {
	Conns []Observation
}

// Observation is one connection as the merged mesh shows it.
type Observation struct {
	// Host and ConnID are the anchor end: the client half when the client
	// was traced, else the server half. Both are stable across rebuilds
	// of the same traces, so they key the row.
	Host, ConnID string
	Role         string // the anchor's role: RoleClient, RoleServer, RoleUnknown
	State        string // schema.StateFused or schema.StateUnresolved
	Reason       string // the merge's reason, when unresolved
	MergedID     string // the merged conn id, when fused
	Project      string // the anchor's project
	// Client and Server are the two ends; an untraced end is zero.
	Client, Server End
	// Endpoint is the server address as the anchor saw it: the client's
	// peer, or the server's own local address. Across NAT the two ends
	// see different addresses; the client's view is what the client
	// app talks to, so it wins when the client was traced.
	Endpoint netip.AddrPort
	// Hostname is the name the client asked for: its HTTP requests' host
	// when that is a name, else — a best guess, as one address may serve
	// many names — its latest DNS lookup that resolved to the endpoint's
	// address before the connection; "" when neither said.
	Hostname string
	// TLS is 1 for https requests, 0 for plain http, -1 when no request
	// said (non-HTTP traffic, or no agent).
	TLS int
	// First and Last bound the connection's records (wall ns).
	First, Last uint64
	Requests    []Request // both ends' views, sorted by (Source, Method, Scheme, Host, Path)
	// Sources are the sources whose records name the connection, either
	// end's, sorted by Name: what DB.Forget drops when a segment is
	// pruned (task gk4). A request's source counts as one too.
	Sources []SourceSpan
}

// SourceSpan is one source's records of a connection: the source's
// name and the window (wall ns) its records span.
type SourceSpan struct {
	Name        string
	First, Last uint64
}

// End is one traced end of a connection.
type End struct {
	Project, Host, ConnID string
	// App is the app node id ("app:<project>/<pattern>") of the end's
	// process, "" for a -pid or -listen-port session; Comm is the
	// process name, the label when there is no app.
	App, Comm string
}

// Request counts the HTTP requests of one connection that one source
// recorded with one method, scheme, host and path template (the redacted
// path with its id-like segments folded, internal/pathtmpl). Scheme is
// "https" over TLS, as the library saw it at its API (never read off the
// wire). Host is set only by the client's end, so a request with a host
// is the client's view and one without the server's; both views are
// kept, and the reads count one (the larger), never their sum.
type Request struct {
	// Source is the federate.Source the events came from (its Name): the
	// unit counts are exact for. Sources are expected to be immutable
	// once collected (a published segment, a finished trace directory);
	// one that grows is fine too, its count only rises.
	Source                     string
	Method, Scheme, Host, Path string
	Count                      uint64
	First, Last                uint64 // wall ns
}

// Observe turns a merge into a Snapshot. The merge says which halves
// fused (r.Conns) and which stayed unresolved (r.Unresolved), and its
// graph names apps and processes; the sources are read once more for
// what the merge drops — HTTP requests, DNS lookups and each
// connection's last record — and for same-host pairs, which the host's
// own correlator fused before the merge (they are no halves). Requests
// are counted per path template (paths: the operator's rules, nil for
// the built-in folding alone), so the database holds one row per route
// rather than one per id; the events keep the raw path (decision #22).
func Observe(ctx context.Context, sources []federate.Source, r federate.Result,
	paths *pathtmpl.Rules) (Snapshot, error) {
	t := newTraffic()
	t.paths = paths
	for _, s := range sources {
		t.source = s.Name()
		if err := s.Events(ctx, t.add); err != nil {
			return Snapshot{}, err
		}
	}
	b := builder{t: t, procs: procInfo(r.Graph), renames: r.Renames}
	var out []Observation
	for _, c := range r.Conns {
		out = append(out, b.fused(c))
	}
	for _, u := range r.Unresolved {
		out = append(out, b.unresolved(u))
	}
	out = append(out, b.localPairs()...)
	slices.SortFunc(out, func(a, b Observation) int {
		return cmp.Or(cmp.Compare(a.Host, b.Host), cmp.Compare(a.ConnID, b.ConnID))
	})
	return Snapshot{Conns: out}, nil
}

// proc is what the merged graph says about one process node.
type proc struct{ app, comm string }

// procInfo maps every process node id to its app (via the incarnation
// edges) and its comm.
func procInfo(g schema.Graph) map[string]proc {
	out := map[string]proc{}
	for _, n := range g.Nodes {
		if n.Type == schema.NodeProcess {
			out[n.ID] = proc{comm: n.Attrs["comm"]}
		}
	}
	for _, e := range g.Edges {
		if e.Type == schema.EdgeIncarnation {
			p := out[e.To]
			p.app = e.From
			out[e.To] = p
		}
	}
	return out
}

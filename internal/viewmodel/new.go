package viewmodel

import (
	"cmp"
	"maps"
	"slices"
	"time"

	"iormesh/internal/federate"
	"iormesh/internal/report"
	"iormesh/internal/routemap"
	"iormesh/internal/servicedb"
)

// Input is what one build hands the model: its own reading (Tally,
// Merge, the segments it skipped) and the service database's view of
// the projects built (Endpoints, Interactions, read after the build's
// import). The database is additive, so services and edges cover every
// build of these projects so far, not only the segments still in the
// store — the who-talks-to-whom index keeps what was deleted.
type Input struct {
	Projects     []string
	Tally        *Tally
	Merge        federate.Result
	BadSegments  []federate.BadSegment
	Ignored      []string
	Endpoints    []servicedb.Endpoint
	Interactions []servicedb.Interaction
	// Observations are this build's connections as servicedb.Observe saw
	// them: what names a conversation's endpoints. Conversations is the
	// build's recording, planned and read (Recorder); nil for none.
	Observations  []servicedb.Observation
	Conversations *Recorder
	// Report is the build's rollup (internal/report): the processes
	// view is projected from it; nil for none.
	Report *report.Report
	// Routes is the compiled route map (internal/routemap): the routed
	// edges are joined from it and Observations; nil for none.
	Routes *routemap.Table
	// Containers are the pods' container maps by store host directory
	// (pod-<uid>), each a runtime id → container name
	// (federate.ContainersFile, task jk4): what names a process's
	// container; nil, or a directory without one, leaves the id.
	Containers map[string]map[string]string
}

// New builds the model. Every list is sorted, so equal inputs give equal
// models.
func New(in Input) Model {
	m := Model{Run: Run{Projects: slices.Sorted(slices.Values(in.Projects))}}
	if in.Tally != nil {
		in.Tally.fill(&m)
	}
	m.Run.FusedConns = len(in.Merge.Conns)
	for _, u := range in.Merge.Unresolved {
		if u.Reason == federate.ReasonPending {
			m.Run.PendingConns++
		} else {
			m.Run.UnresolvedConns++
		}
	}
	m.Run.GraphNodes, m.Run.GraphEdges = len(in.Merge.Graph.Nodes), len(in.Merge.Graph.Edges)
	m.Health.Clocks = clocks(in.Merge.Clocks, fqdns(in.Tally))
	for _, b := range in.BadSegments {
		m.Health.BadSegments = append(m.Health.BadSegments, BadSegment{Name: b.Name, Error: b.Err.Error()})
	}
	m.Health.Ignored = slices.Sorted(slices.Values(in.Ignored))
	m.Services = services(in.Endpoints, in.Interactions)
	m.Edges = edges(in.Interactions)
	m.Conversations, m.Threads = []Conversation{}, []ThreadStory{}
	n := newNamer(in.Merge.Graph, in.Observations, in.Endpoints)
	if r := in.Conversations; r != nil {
		m.Conversations, m.Threads = r.conversations(n)
		m.MoreConversations, m.Health.RequestsCut = r.more, r.cut
	}
	m.Processes, m.MoreProcesses = processes(in.Report, n, in.Containers)
	if in.Report != nil {
		m.Untracked = in.Report.Untracked
	}
	m.Routes, m.Health.RouteFlags = routes(in.Routes, in.Observations, in.Merge.Conns, in.Endpoints,
		tracedHosts(in.Tally, in.Observations))
	return m
}

// fqdns is the tally's host id → FQDN map, empty without a tally.
func fqdns(t *Tally) map[string]string {
	if t == nil {
		return nil
	}
	return t.fqdns
}

// clocks summarizes the bounds per host pair; federate sorts them by
// (HostA, HostB, Start), so pairs come out in host-id order.
func clocks(bounds []federate.ClockBound, names map[string]string) []Clock {
	var out []Clock
	for _, b := range bounds {
		a, bb := cmp.Or(names[b.HostA], b.HostA), cmp.Or(names[b.HostB], b.HostB)
		if n := len(out); n == 0 || out[n-1].HostA != a || out[n-1].HostB != bb {
			out = append(out, Clock{HostA: a, HostB: bb})
		}
		c := &out[len(out)-1]
		c.Windows++
		w, ok := b.Width()
		switch {
		case b.Empty():
			c.Empty++
		case !ok:
			c.OneSided++
		default:
			d := time.Duration(w)
			if c.MinWidth == 0 || d < c.MinWidth {
				c.MinWidth = d
			}
			c.MaxWidth = max(c.MaxWidth, d)
		}
	}
	return out
}

// services groups the endpoints by label and adds every client name the
// edges show that serves no endpoint, sorted by name.
func services(eps []servicedb.Endpoint, ins []servicedb.Interaction) []Service {
	byName := map[string]*Service{}
	paths := map[string]map[[2]string]int64{}
	for _, e := range eps {
		name := e.Label()
		s := byName[name]
		if s == nil {
			s = &Service{Name: name, Named: e.Service != "", TLS: e.TLS}
			byName[name], paths[name] = s, map[[2]string]int64{}
		}
		s.Endpoints = append(s.Endpoints, endpointString(e))
		s.Conns += e.Conns
		s.Unresolved += e.Unresolved
		s.TLS = mergeTLS(s.TLS, e.TLS)
		for _, r := range e.Requests {
			s.Requests += r.Count
			paths[name][[2]string{r.Method, r.Path}] += r.Count
		}
	}
	for _, in := range ins {
		if byName[in.From] == nil {
			byName[in.From] = &Service{Name: in.From}
		}
	}
	out := make([]Service, 0, len(byName))
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		s := byName[name]
		slices.Sort(s.Endpoints)
		s.TopPaths, s.MorePaths = topPaths(paths[name])
		out = append(out, *s)
	}
	return out
}

// topPaths returns the MaxTopPaths most requested paths, by count then
// method and path, and how many more there were.
func topPaths(counts map[[2]string]int64) ([]PathStat, int) {
	var all []PathStat
	for k, n := range counts {
		all = append(all, PathStat{Method: k[0], Path: k[1], Count: n})
	}
	slices.SortFunc(all, func(a, b PathStat) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Method, b.Method),
			cmp.Compare(a.Path, b.Path))
	})
	if len(all) > MaxTopPaths {
		return all[:MaxTopPaths], len(all) - MaxTopPaths
	}
	return all, 0
}

// edges turns the database's interactions (sorted by From, To) into
// edges with a state.
func edges(ins []servicedb.Interaction) []Edge {
	out := make([]Edge, 0, len(ins))
	for _, in := range ins {
		state := StateMixed
		switch in.Unresolved {
		case 0:
			state = StateFused
		case in.Conns:
			state = StateUnresolved
		}
		out = append(out, Edge{From: in.From, To: in.To, State: state, Conns: in.Conns,
			Unresolved: in.Unresolved, First: wallTime(in.First), Last: wallTime(in.Last)})
	}
	return out
}

// endpointString is hostname:port when the endpoint has a name, else
// ip:port (IPv6 bracketed), ending in "@host" for a loopback or
// link-local endpoint: the endpoint's Label without its service.
func endpointString(e servicedb.Endpoint) string {
	e.Service = ""
	return e.Label()
}

// mergeTLS combines two TLS labels: unknown yields to known, and two
// different known ones are mixed.
func mergeTLS(a, b string) string {
	switch {
	case a == "" || a == b:
		return b
	case b == "":
		return a
	}
	return "mixed"
}

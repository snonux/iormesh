package viewmodel

import (
	"slices"

	"iormesh/internal/termsafe"
)

// Safe is safeModel for a renderer outside this package: the TUI
// (internal/tui) makes the whole model terminal-safe once when it loads
// it, so no screen, row or detail line can print a server's string raw.
func Safe(m Model) Model { return safeModel(m) }

// safeModel returns a copy of m whose every string is terminal-safe
// (termsafe.String). Nearly all of them come from the servers — service
// and client names (hostnames, comms), FQDNs, end reasons, bad segments'
// names and errors, stray file names — and a compromised server could
// put escape sequences in any. Done once over the whole model before a
// text writer formats it, so a field added later cannot be missed by one
// writer. The model itself (model.json) stays unchanged: JSON is data,
// not terminal text. encoding/json escapes only bytes below 0x20, not
// DEL, the C1 controls or bidi overrides, so whatever prints JSON to a
// terminal (iormeshctl show -json, query) passes it through
// termsafe.JSON, which escapes those as \u escapes the decoder reads
// back as the same value.
func safeModel(m Model) Model {
	s := termsafe.String
	m.Run.Projects, m.Run.Hosts = safeAll(m.Run.Projects), safeAll(m.Run.Hosts)
	m.Run.Window = s(m.Run.Window) // build's own text, but read back from a file
	h := &m.Health
	h.Sessions = slices.Clone(h.Sessions)
	for i := range h.Sessions {
		x := &h.Sessions[i]
		x.Project, x.Host, x.FQDN, x.ID, x.End = s(x.Project), s(x.Host), s(x.FQDN), s(x.ID), s(x.End)
		x.HostID = s(x.HostID)
	}
	h.Clocks = slices.Clone(h.Clocks)
	for i := range h.Clocks {
		h.Clocks[i].HostA, h.Clocks[i].HostB = s(h.Clocks[i].HostA), s(h.Clocks[i].HostB)
	}
	h.BadSegments = slices.Clone(h.BadSegments)
	for i := range h.BadSegments {
		h.BadSegments[i].Name, h.BadSegments[i].Error = s(h.BadSegments[i].Name), s(h.BadSegments[i].Error)
	}
	h.Ignored = safeAll(h.Ignored)
	m.Services = slices.Clone(m.Services)
	for i := range m.Services {
		x := &m.Services[i]
		x.Name, x.TLS, x.Endpoints = s(x.Name), s(x.TLS), safeAll(x.Endpoints)
		x.TopPaths = slices.Clone(x.TopPaths)
		for j := range x.TopPaths {
			x.TopPaths[j].Method, x.TopPaths[j].Path = s(x.TopPaths[j].Method), s(x.TopPaths[j].Path)
		}
	}
	m.Edges = slices.Clone(m.Edges)
	for i := range m.Edges {
		m.Edges[i].From, m.Edges[i].To, m.Edges[i].State = s(m.Edges[i].From), s(m.Edges[i].To), s(m.Edges[i].State)
	}
	m.Conversations = slices.Clone(m.Conversations)
	for i := range m.Conversations {
		m.Conversations[i] = safeConversation(m.Conversations[i])
	}
	m.Threads = slices.Clone(m.Threads)
	for i := range m.Threads {
		m.Threads[i] = safeStory(m.Threads[i])
	}
	m.Processes = slices.Clone(m.Processes)
	for i := range m.Processes {
		m.Processes[i] = safeProcess(m.Processes[i])
	}
	m.Routes, h.RouteFlags = safeRoutes(m.Routes, h.RouteFlags)
	return m
}

// safeLifeline is l with its strings terminal-safe.
func safeLifeline(l Lifeline) Lifeline {
	s := termsafe.String
	l.Service, l.Host, l.Thread = s(l.Service), s(l.Host), s(l.Thread)
	return l
}

// safeStory is t with every string terminal-safe.
func safeStory(t ThreadStory) ThreadStory {
	s := termsafe.String
	t.Lifeline = safeLifeline(t.Lifeline)
	t.Entries = slices.Clone(t.Entries)
	for i := range t.Entries {
		x := &t.Entries[i]
		x.Conversation, x.Act, x.Label, x.Peer = s(x.Conversation), s(x.Act), s(x.Label), s(x.Peer)
		x.Ordering, x.Ref = s(x.Ordering), s(x.Ref)
	}
	return t
}

// safeProcess is p with every string terminal-safe: comms, apps, paths,
// addresses and peers all come from the servers.
func safeProcess(p Process) Process {
	s := termsafe.String
	p.ID, p.Project, p.Host, p.FQDN, p.Comm, p.App = s(p.ID), s(p.Project), s(p.Host), s(p.FQDN), s(p.Comm), s(p.App)
	p.Service, p.Serves = s(p.Service), safeAll(p.Serves)
	// Both are checked where they are read (schema, the container map);
	// made safe here too so no renderer depends on that.
	p.ContainerID, p.Container = s(p.ContainerID), s(p.Container)
	p.Threads = slices.Clone(p.Threads)
	for i := range p.Threads {
		p.Threads[i].Name = s(p.Threads[i].Name)
	}
	p.Files = slices.Clone(p.Files)
	for i := range p.Files {
		p.Files[i].Path, p.Files[i].Ops = s(p.Files[i].Path), safeAll(p.Files[i].Ops)
	}
	p.Sockets = slices.Clone(p.Sockets)
	for i := range p.Sockets {
		x := &p.Sockets[i]
		x.ID, x.Role, x.Local, x.Remote, x.Service = s(x.ID), s(x.Role), s(x.Local), s(x.Remote), s(x.Service)
		x.PeerState, x.Peers = s(x.PeerState), safeAll(x.Peers)
	}
	return p
}

func safeAll(ss []string) []string {
	out := slices.Clone(ss)
	for i := range out {
		out[i] = termsafe.String(out[i])
	}
	return out
}

package viewmodel

import (
	"slices"

	"iormesh/internal/termsafe"
)

// safeModel returns a copy of m whose every string is terminal-safe
// (termsafe.String). Nearly all of them come from the servers — service
// and client names (hostnames, comms), FQDNs, end reasons, bad segments'
// names and errors, stray file names — and a compromised server could
// put escape sequences in any. Done once over the whole model before a
// text writer formats it, so a field added later cannot be missed by one
// writer; the model itself (model.json, -json) stays unchanged, as JSON
// escapes control characters anyway.
func safeModel(m Model) Model {
	s := termsafe.String
	m.Run.Projects, m.Run.Hosts = safeAll(m.Run.Projects), safeAll(m.Run.Hosts)
	h := &m.Health
	h.Sessions = slices.Clone(h.Sessions)
	for i := range h.Sessions {
		x := &h.Sessions[i]
		x.Project, x.Host, x.FQDN, x.ID, x.End = s(x.Project), s(x.Host), s(x.FQDN), s(x.ID), s(x.End)
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
	return m
}

func safeAll(ss []string) []string {
	out := slices.Clone(ss)
	for i := range out {
		out[i] = termsafe.String(out[i])
	}
	return out
}

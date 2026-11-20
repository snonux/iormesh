package render

import (
	"strings"

	"iormesh/internal/viewmodel"
)

// Names are the view model's names for the merged graph's nodes, so
// graph.svg speaks the same names as every other surface (PLAN.md M5:
// "named services render by name in graph and SVG"; task rb4 found the
// export drawing machine ids and "java PID" while `show`, the TUI and
// the site said "orders" on "hostb"). The graph itself carries neither:
// host names come from the store's host directories and service names
// from the service database, both of which only the build knows.
type Names struct {
	// Hosts maps a host id to the store's host directory name.
	Hosts map[string]string
	// Services maps a process node id to the services it serves, else
	// to its lifeline name when that says more than its comm (its app).
	Services map[string]string
}

// NamesOf collects the names from the model's processes: each carries
// its graph node id, host directory, the services it serves and its
// lifeline name. A server is named by what it serves, as the
// conversations' server lifelines are: its lifeline name is its client
// name, which without an app is only its comm. Processes the model cut
// (MoreProcesses) keep their bare labels, as before.
func NamesOf(m viewmodel.Model) Names {
	n := Names{Hosts: map[string]string{}, Services: map[string]string{}}
	for _, p := range m.Processes {
		if host, _, ok := strings.Cut(strings.TrimPrefix(p.ID, "proc:"), "/"); ok && p.Host != "" {
			n.Hosts[host] = p.Host
		}
		switch {
		case len(p.Serves) > 0:
			n.Services[p.ID] = strings.Join(p.Serves, ",")
		case p.Service != "" && p.Service != p.Comm:
			n.Services[p.ID] = p.Service
		}
	}
	return n
}

// host is a host id's name, else the id.
func (n Names) host(id string) string {
	if name := n.Hosts[id]; name != "" {
		return name
	}
	return id
}

// hostLabel labels a host node: its name with the id after it, so two
// emulated hosts of one name are still told apart; the bare id unnamed.
func (n Names) hostLabel(id string) string {
	if name := n.Hosts[id]; name != "" && name != id {
		return name + " " + id
	}
	return id
}

// process prefixes a process label with its service.
func (n Names) process(id, label string) string {
	if svc := n.Services[id]; svc != "" {
		return svc + ": " + label
	}
	return label
}

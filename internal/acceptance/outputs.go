package acceptance

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"iormesh/internal/schema"
	"iormesh/internal/viewmodel"
)

// Paths under iormeshctl's HOME (the defaults it derives from HOME; the
// run passes no path flag, as an operator would not).
func (r *Runner) dataDir() string  { return filepath.Join(r.cfg.WorkDir, ".local", "share", "iormesh") }
func (r *Runner) storeDir() string { return filepath.Join(r.dataDir(), "traces") }
func (r *Runner) registryPath() string {
	return filepath.Join(r.cfg.WorkDir, ".config", "iormesh", "servers.yaml")
}

// buildDir is where `build -project …` writes; the key joins the sorted
// projects with '+' as iormeshctl does (internal/ctl buildKey).
func (r *Runner) buildDir(projects []string) string {
	ps := slices.Clone(projects)
	slices.Sort(ps)
	return filepath.Join(r.dataDir(), "builds", strings.Join(ps, "+"))
}

// build runs `iormeshctl build -project …` and reads its outputs.
func (r *Runner) build(ctx context.Context, projects ...string) (viewmodel.Model, schema.Graph, error) {
	args := []string{"build"}
	for _, p := range projects {
		args = append(args, "-project", p)
	}
	if _, err := r.ctlRun(ctx, args...); err != nil {
		return viewmodel.Model{}, schema.Graph{}, err
	}
	return readBuild(r.buildDir(projects))
}

// readBuild reads a build directory's model.json and graph.json.
func readBuild(dir string) (viewmodel.Model, schema.Graph, error) {
	var m viewmodel.Model
	data, err := os.ReadFile(filepath.Join(dir, "model.json"))
	if err != nil {
		return m, schema.Graph{}, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, schema.Graph{}, fmt.Errorf("model.json: %w", err)
	}
	data, err = os.ReadFile(filepath.Join(dir, "graph.json"))
	if err != nil {
		return m, schema.Graph{}, err
	}
	g, err := schema.DecodeGraph(data)
	if err != nil {
		return m, g, fmt.Errorf("graph.json: %w", err)
	}
	return m, g, nil
}

// fusedCrossHost returns the conn ids of the graph's fused sockets whose
// connected processes run on two different hosts.
func fusedCrossHost(g schema.Graph) []string {
	hosts := map[string]map[string]bool{} // conn node -> host ids
	for _, e := range g.Edges {
		if e.Type != "connected" || e.State != "fused" {
			continue
		}
		if hosts[e.To] == nil {
			hosts[e.To] = map[string]bool{}
		}
		hosts[e.To][procHost(e.From)] = true
	}
	var ids []string
	for conn, hs := range hosts {
		if len(hs) == 2 {
			ids = append(ids, strings.TrimPrefix(conn, "conn:"))
		}
	}
	slices.Sort(ids)
	return ids
}

// procHost is the host id of a process node id, proc:<host_id>/<pid>@<inc>.
func procHost(id string) string {
	h, _, _ := strings.Cut(strings.TrimPrefix(id, "proc:"), "/")
	return h
}

// readSlice decodes `iormeshctl query` output strictly, refusing a cut
// slice: every check that reads one needs all of it.
func readSlice(out string) ([]schema.Event, error) {
	var evs []schema.Event
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if strings.Contains(string(line), `"event":"truncated"`) {
			return nil, fmt.Errorf("query slice cut at %d events: raise -max-bytes", len(evs))
		}
		ev, err := schema.DecodeEvent(line)
		if err != nil {
			return nil, fmt.Errorf("query line %d: %w", len(evs)+1, err)
		}
		evs = append(evs, ev)
	}
	return evs, sc.Err()
}

// natEnds returns the client's dialed port (connect's remote_port) and
// the server's accepted port (accept's local_port) on one connection's
// events; 0 when that end has none.
func natEnds(evs []schema.Event) (dialed, accepted uint16) {
	for _, e := range evs {
		switch {
		case e.Event == "connect" && e.Args.RemotePort != nil:
			dialed = *e.Args.RemotePort
		case (e.Event == "accept" || e.Event == "accept4") && e.Args.LocalPort != nil && e.Ret >= 0:
			accepted = *e.Args.LocalPort
		}
	}
	return dialed, accepted
}

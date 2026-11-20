package federate

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/schema"
)

// rebuildEvent is a minimal event of pid on host h.
func rebuildEvent(pid uint32, kind string, fd int32, ret int64) schema.Event {
	return schema.Event{SchemaVersion: schema.Version, Project: "shop", HostID: "h", PID: pid,
		PIDInc: 1, TID: pid, Comm: "app", Event: kind, FD: fd, Ret: ret, Attribution: schema.AttributionNone}
}

func withPath(ev schema.Event, path string) schema.Event {
	ev.Args.Path = ptr(path)
	return ev
}

func withConn(ev schema.Event, conn string) schema.Event {
	ev.ConnID = conn
	return ev
}

// edgeList renders a graph's edges as "type from to state" lines.
func edgeList(g schema.Graph) []string {
	var out []string
	for _, e := range g.Edges {
		out = append(out, e.Type+" "+e.From+" "+e.To+" "+e.State)
	}
	return out
}

// TestRebuildRules pins the correlator rules the rebuild applies (one
// case per rule; rebuild.go lists what is out of reach).
func TestRebuildRules(t *testing.T) {
	p1, p2 := schema.ProcessNodeID("h", 1, 1), schema.ProcessNodeID("h", 2, 1)
	ren := rebuildEvent(1, schema.KindRename, schema.NoFD, 0)
	ren.Args.Path, ren.Args.NewPath = ptr("/d/a.tmp"), ptr("/d/a")
	sig := rebuildEvent(2, schema.KindSignal, schema.NoFD, 0)
	sig.Args.SenderPID = ptr(uint32(1))
	for _, tc := range []struct {
		name string
		evs  []schema.Event
		want []string // the edges besides runs_on
	}{
		{"open and failed open", []schema.Event{withPath(rebuildEvent(1, schema.KindOpenat, 3, 3), "/etc/x"),
			withPath(rebuildEvent(1, schema.KindOpenat, -1, -2), "/etc/missing")},
			[]string{"opened " + p1 + " file:h:/etc/x "}},
		{"rename moves an open fd", []schema.Event{withPath(rebuildEvent(1, schema.KindCreat, 4, 4), "/d/a.tmp"), ren},
			[]string{"opened " + p1 + " file:h:/d/a ", "opened " + p1 + " file:h:/d/a.tmp "}},
		{"a closed fd does not move", []schema.Event{withPath(rebuildEvent(1, schema.KindCreat, 4, 4), "/d/a.tmp"),
			rebuildEvent(1, schema.KindClose, 4, 0), ren},
			[]string{"opened " + p1 + " file:h:/d/a.tmp "}},
		{"pipe is fused", []schema.Event{withConn(rebuildEvent(1, schema.KindPipe, 5, 0), "p"),
			withConn(rebuildEvent(2, schema.KindRead, 5, 1), "p")},
			[]string{"connected " + p1 + " conn:p fused", "connected " + p2 + " conn:p fused"}},
		{"loopback pair is fused", []schema.Event{withConn(rebuildEvent(1, schema.KindConnect, 5, 0), "lo"),
			withConn(rebuildEvent(2, schema.KindAccept4, 6, 6), "lo")},
			[]string{"connected " + p1 + " conn:lo fused", "connected " + p2 + " conn:lo fused"}},
		{"one end is unresolved", []schema.Event{withConn(rebuildEvent(1, schema.KindConnect, 5, 0), "x")},
			[]string{"connected " + p1 + " conn:x unresolved"}},
		{"signal from a traced sender", []schema.Event{rebuildEvent(1, schema.KindRead, 0, 0), sig},
			[]string{"signaled " + p1 + " " + p2 + " "}},
		{"signal from an untraced sender", []schema.Event{sig}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGraphRebuilder()
			for i := range tc.evs {
				g.add(&tc.evs[i])
			}
			got := slices.DeleteFunc(edgeList(g.graph()), func(s string) bool {
				return strings.HasPrefix(s, schema.EdgeRunsOn+" ")
			})
			if !slices.Equal(got, tc.want) {
				t.Errorf("edges = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRebuildFDCopies: a dup'd fd follows a rename like the original;
// a close-on-exec fd an exec closed does not.
func TestRebuildFDCopies(t *testing.T) {
	p1 := schema.ProcessNodeID("h", 1, 1)
	ren := rebuildEvent(1, schema.KindRename, schema.NoFD, 0)
	ren.Args.Path, ren.Args.NewPath = ptr("/d/a.tmp"), ptr("/d/a")
	open := withPath(rebuildEvent(1, schema.KindOpenat, 4, 4), "/d/a.tmp")
	cloexecOpen := open
	cloexecOpen.Args.Flags = ptr(uint32(oCloexec))
	dup := rebuildEvent(1, schema.KindDup, 4, 9)
	fcntl := rebuildEvent(1, schema.KindFcntl, 4, 10)
	fcntl.Args.Cmd = ptr(uint32(fDupFDCloexec))
	exec := rebuildEvent(1, schema.KindExec, schema.NoFD, 0)
	moved := []string{"opened " + p1 + " file:h:/d/a ", "opened " + p1 + " file:h:/d/a.tmp "}
	for _, tc := range []struct {
		name string
		evs  []schema.Event
		want []string
	}{
		{"dup then close the original", []schema.Event{open, dup, rebuildEvent(1, schema.KindClose, 4, 0), ren}, moved},
		{"exec keeps a plain fd", []schema.Event{open, exec, ren}, moved},
		{"exec closes an O_CLOEXEC fd", []schema.Event{cloexecOpen, exec, ren}, moved[1:]},
		{"exec closes an F_DUPFD_CLOEXEC copy", []schema.Event{open, fcntl,
			rebuildEvent(1, schema.KindClose, 4, 0), exec, ren}, moved[1:]},
	} {
		g := newGraphRebuilder()
		for i := range tc.evs {
			g.add(&tc.evs[i])
		}
		got := slices.DeleteFunc(edgeList(g.graph()), func(s string) bool {
			return strings.HasPrefix(s, schema.EdgeRunsOn+" ")
		})
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: edges = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestRebuildCorrelatorExport: the correlator's own export golden
// (internal/correlate/testdata) rebuilt from its events is its graph.
func TestRebuildCorrelatorExport(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "correlate", "testdata", "export.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	evs, err := schema.ReadNDJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("..", "correlate", "testdata", "export.graph.json"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := schema.DecodeGraph(data)
	if err != nil {
		t.Fatal(err)
	}
	g := newGraphRebuilder()
	for i := range evs {
		g.add(&evs[i])
	}
	if got := graphText(t, g.graph()); got != graphText(t, want) {
		t.Errorf("rebuilt\n%s\nwant\n%s", got, graphText(t, want))
	}
}

// TestRebuildCommIsNewest: an exec renames the process node, as the
// correlator's does; an event without comm leaves it.
func TestRebuildCommIsNewest(t *testing.T) {
	g := newGraphRebuilder()
	evs := []schema.Event{rebuildEvent(1, schema.KindRead, 0, 0), rebuildEvent(1, schema.KindExec, -1, 0),
		rebuildEvent(1, schema.KindRead, 0, 0)}
	evs[1].Comm, evs[2].Comm = "java", ""
	for i := range evs {
		g.add(&evs[i])
	}
	for _, n := range g.graph().Nodes {
		if n.Type == schema.NodeProcess && n.Attrs["comm"] != "java" {
			t.Fatalf("process node %+v, want comm java", n)
		}
	}
}

// TestRebuildLifecycle pins the task ze4 records: a fork's forked edge and
// inherited fds, an attach's app and incarnation edge, F_SETFD's flags,
// and a loopback UDP flow fused by its mirrored address pairs.
func TestRebuildLifecycle(t *testing.T) {
	p1, p2 := schema.ProcessNodeID("h", 1, 1), schema.ProcessNodeID("h", 2, 1)
	ren := rebuildEvent(1, schema.KindRename, schema.NoFD, 0)
	ren.Args.Path, ren.Args.NewPath = ptr("/d/a.tmp"), ptr("/d/a")
	open := withPath(rebuildEvent(1, schema.KindOpenat, 4, 4), "/d/a.tmp")
	fork := rebuildEvent(2, schema.KindFork, schema.NoFD, 0)
	fork.Args.ParentPID, fork.Args.ParentInc = ptr(uint32(1)), ptr(uint64(1))
	orphan := rebuildEvent(2, schema.KindFork, schema.NoFD, 0)
	attach := rebuildEvent(1, schema.KindAttach, schema.NoFD, 0)
	attach.Args.Pattern = ptr("*app*")
	setfd := rebuildEvent(1, schema.KindFcntl, 4, 0)
	setfd.Args.Cmd, setfd.Args.Flags = ptr(uint32(fSetFD)), ptr(uint32(fdCloexec))
	exec := rebuildEvent(2, schema.KindExec, schema.NoFD, 0)
	app := schema.AppNodeID("shop", "*app*")
	for _, tc := range []struct {
		name string
		evs  []schema.Event
		want []string
	}{
		{"a fork's child moves an inherited fd", []schema.Event{open, fork, rebuildEvent(1, schema.KindClose, 4, 0), ren},
			[]string{"forked " + p1 + " " + p2 + " ", "opened " + p1 + " file:h:/d/a.tmp ", "opened " + p2 + " file:h:/d/a "}},
		{"a fork without a parent inherits nothing", []schema.Event{open, orphan, rebuildEvent(1, schema.KindClose, 4, 0), ren},
			[]string{"opened " + p1 + " file:h:/d/a.tmp "}},
		{"F_SETFD close-on-exec, then the child's exec", []schema.Event{open, setfd, fork,
			rebuildEvent(1, schema.KindClose, 4, 0), exec, ren},
			[]string{"forked " + p1 + " " + p2 + " ", "opened " + p1 + " file:h:/d/a.tmp "}},
		{"an attach with a pattern", []schema.Event{attach}, []string{"incarnation " + app + " " + p1 + " "}},
		{"an attach without one", []schema.Event{rebuildEvent(1, schema.KindAttach, schema.NoFD, 0)}, nil},
		{"a loopback UDP flow", []schema.Event{udp(1, "p", 40000, 9000), udp(2, "p", 9000, 40000)},
			[]string{"connected " + p1 + " conn:p fused", "connected " + p2 + " conn:p fused"}},
		{"a UDP flow to another host", []schema.Event{udp(1, "q", 40000, 9000)},
			[]string{"connected " + p1 + " conn:q unresolved"}},
	} {
		got := slices.DeleteFunc(edgeList(RebuildGraph(tc.evs)), func(s string) bool {
			return strings.HasPrefix(s, schema.EdgeRunsOn+" ")
		})
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: edges = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// udp is a sendto of pid on conn from a wildcard-bound local port to a
// loopback peer, as the correlator writes a UDP flow's datagram.
func udp(pid uint32, conn string, local, remote uint16) schema.Event {
	ev := withConn(rebuildEvent(pid, schema.KindSendto, 5, 1), conn)
	ev.Args.LocalPort, ev.Args.RemotePort = ptr(local), ptr(remote)
	ev.Args.RemoteAddr = ptr("127.0.0.1")
	if conn == "q" {
		ev.Args.RemoteAddr = ptr("192.0.2.1")
	}
	return ev
}

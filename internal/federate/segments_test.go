package federate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"iormesh/internal/schema"
	"iormesh/internal/segment"
)

// segT0 is the fake clock the test segments are written by.
var segT0 = time.Date(2026, 11, 20, 6, 30, 0, 0, time.UTC)

// meshHosts places each golden trace in the store, as collect would:
// <project>/<registry host>/.
var meshHosts = map[string][2]string{
	"a": {"shop", "app1"}, "b": {"billing", "app2"}, "c": {"shop", "app3"}, "a-batch": {"batch", "app1"},
}

// writeSegments writes tr's events with the tracer's segment writer,
// rotating after every perSeg events, and moves the published files into
// store/<project>/<host>/ as collect does. It returns the events files'
// paths in seq order.
func writeSegments(t *testing.T, store, host, session string, tr *trace, perSeg int) []string {
	t.Helper()
	srv := filepath.Join(t.TempDir(), tr.project)
	w, err := segment.New(segment.Config{Dir: srv, FQDN: tr.host + ".example.org", Session: session,
		Project: tr.project, Now: func() time.Time { return segT0 }})
	if err != nil {
		t.Fatal(err)
	}
	for i := range tr.events {
		if err := w.Event(&tr.events[i]); err != nil {
			t.Fatal(err)
		}
		if (i+1)%perSeg == 0 {
			if err := w.Tick(segT0.Add(time.Duration(i+1) * time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return moveReady(t, filepath.Join(srv, segment.ReadyDir), filepath.Join(store, tr.project, host))
}

// moveReady moves every file of ready into dst and returns the events
// files' new paths, sorted.
func moveReady(t *testing.T, ready, dst string) []string {
	t.Helper()
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(ready)
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	for _, en := range ents {
		to := filepath.Join(dst, en.Name())
		if err := os.Rename(filepath.Join(ready, en.Name()), to); err != nil {
			t.Fatal(err)
		}
		if f, err := segment.ParseName(en.Name()); err == nil && f.Table == "" {
			events = append(events, to)
		}
	}
	slices.Sort(events)
	return events
}

// meshStore writes the golden scenario as a collected store, a few
// events per segment so every session spans several.
func meshStore(t *testing.T) string {
	t.Helper()
	store := t.TempDir()
	for name, tr := range meshTraces() {
		writeSegments(t, store, meshHosts[name][1], "a1b2c3d4", tr, 3)
	}
	return store
}

// TestSegmentMergeGolden: the golden mesh written as parquet segments
// and merged from the store gives the NDJSON merge's result byte for byte
// (testdata/mesh.golden) — the graph rebuilt from the events included.
func TestSegmentMergeGolden(t *testing.T) {
	segs, err := LoadSegments(context.Background(), meshStore(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs.Bad) != 0 || len(segs.Ignored) != 0 {
		t.Fatalf("bad %v, ignored %v", segs.Bad, segs.Ignored)
	}
	srcs := make([]Source, len(segs.Sources))
	for i, s := range segs.Sources {
		srcs[i] = s
	}
	r, err := Merge(context.Background(), srcs, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "mesh.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if got := report(t, r); got != string(want) {
		t.Fatalf("segment merge differs from testdata/mesh.golden:\n%s", got)
	}
}

// TestRebuildMatchesCorrelatorGraph: for every golden trace, the graph
// rebuilt from its events is the graph its correlator exported.
func TestRebuildMatchesCorrelatorGraph(t *testing.T) {
	for name, tr := range meshTraces() {
		g := newGraphRebuilder()
		for i := range tr.events {
			g.add(&tr.events[i])
		}
		want := schema.Graph{SchemaVersion: schema.Version, Edges: tr.edges}
		for _, n := range tr.nodes {
			want.Nodes = append(want.Nodes, n)
		}
		want.Sort()
		if got := graphText(t, g.graph()); got != graphText(t, want) {
			t.Errorf("%s: rebuilt\n%s\nwant\n%s", name, got, graphText(t, want))
		}
	}
}

func graphText(t *testing.T, g schema.Graph) string {
	t.Helper()
	var b bytes.Buffer
	if err := schema.WriteGraph(&b, g); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestLoadSegmentsSkipsBadOnes: damaged segments and ones whose footer
// disagrees with their place are skipped whole and named, stray entries
// are listed, dot files and table files pass silently, and the damaged
// session's graph is rebuilt from its other segments.
func TestLoadSegmentsSkipsBadOnes(t *testing.T) {
	store := t.TempDir()
	tr := meshTraces()["a"]
	paths := writeSegments(t, store, "app1", "a1b2c3d4", tr, 4)
	if len(paths) < 3 {
		t.Fatalf("only %d segments", len(paths))
	}
	truncate(t, paths[1])
	// A segment of shop copied into billing's directory: its footer says
	// shop, so it is not billing's.
	other := filepath.Join(store, "billing", "app1")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	copyFile(t, paths[2], filepath.Join(other, filepath.Base(paths[2])))
	for _, f := range []string{".rsync-tmp.Xy12", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(store, "shop", "app1", f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	segs, err := LoadSegments(context.Background(), store, nil)
	if err != nil {
		t.Fatal(err)
	}
	var bad []string
	for _, b := range segs.Bad {
		bad = append(bad, b.Name)
	}
	wantBad := []string{"billing/app1/" + filepath.Base(paths[2]), "shop/app1/" + filepath.Base(paths[1])}
	if !slices.Equal(bad, wantBad) {
		t.Errorf("bad = %v, want %v", bad, wantBad)
	}
	if !slices.Equal(segs.Ignored, []string{filepath.Join("shop", "app1", "notes.txt")}) {
		t.Errorf("ignored = %v", segs.Ignored)
	}
	if len(segs.Sources) != len(paths)-1 {
		t.Fatalf("%d good segments, want %d", len(segs.Sources), len(paths)-1)
	}
	g, err := segs.Sources[0].Graph(context.Background())
	if err != nil || len(g.Nodes) == 0 {
		t.Fatalf("graph %v, %v", g, err)
	}
}

// TestLoadSegmentsUnknownProject: a project with nothing collected is an
// error, not an empty build.
func TestLoadSegmentsUnknownProject(t *testing.T) {
	_, err := LoadSegments(context.Background(), t.TempDir(), []string{"shop"})
	if err == nil || !strings.Contains(err.Error(), "nothing collected for project shop") {
		t.Fatalf("err = %v", err)
	}
}

// TestSegmentSessionOfTwoHosts: a session whose segments disagree on the
// host id (a file from another machine renamed into the session) loses
// the odd segment, never merges it as the same host.
func TestSegmentSessionOfTwoHosts(t *testing.T) {
	store := t.TempDir()
	tr := meshTraces()["a"]
	for i := 4; i < 8; i++ { // the second segment's events
		tr.events[i].HostID = "hostZ"
	}
	writeSegments(t, store, "app1", "a1b2c3d4", tr, 4)
	segs, err := LoadSegments(context.Background(), store, []string{"shop"})
	if err != nil {
		t.Fatal(err)
	}
	if len(segs.Bad) != 1 || !strings.Contains(segs.Bad[0].Err.Error(), `host_id "hostZ"`) {
		t.Fatalf("bad = %v", segs.Bad)
	}
}

func truncate(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data[:len(data)/2], 0o600); err != nil {
		t.Fatal(err)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

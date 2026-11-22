package segment

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	parquetgo "github.com/parquet-go/parquet-go"

	"iormesh/internal/schema"
)

// TestReadsSegmentWithoutPodColumns: a segment written before task sh4
// appended the pod object has no pod columns at all; it still reads,
// every event as before and none with a pod (docs/schema.md: appended
// fields are optional, an older file sees them as absent).
func TestReadsSegmentWithoutPodColumns(t *testing.T) {
	full := parquetgo.SchemaOf(schema.Event{})
	group := parquetgo.Group{}
	for _, f := range full.Fields() {
		if f.Name() != "pod" {
			group[f.Name()] = f
		}
	}
	if len(group) != len(full.Fields())-1 {
		t.Fatalf("schema.Event has no pod field to leave out: %d fields", len(group))
	}
	path := filepath.Join(t.TempDir(), "old.parquet")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	meta := map[string]string{"iormesh.schema_version": "1", "iormesh.project": "shop",
		"iormesh.fqdn": "app-1.example.org", "iormesh.session": "a1b2c3d4", "iormesh.seq": "1",
		"iormesh.start": "2026-11-22T10:00:00Z", "iormesh.content": ContentEvents}
	opts := []parquetgo.WriterOption{parquetgo.NewSchema("event", group)}
	for k, v := range meta {
		opts = append(opts, parquetgo.KeyValueMetadata(k, v))
	}
	w := parquetgo.NewGenericWriter[schema.Event](f, opts...)
	want := richEvents()
	if _, err := w.Write(want); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if hasPodColumns(t, path) {
		t.Fatal("the old file has pod columns after all")
	}
	var got []schema.Event
	if _, err := ReadEvents(context.Background(), path, nil, func(ev *schema.Event) error {
		if ev.Pod != nil {
			t.Errorf("event seq %d: pod %+v from a file without pod columns", ev.Seq, ev.Pod)
		}
		got = append(got, *ev)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sameNDJSON(t, got, want)
}

// hasPodColumns reports whether the parquet file at path has the pod
// object's columns.
func hasPodColumns(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	pf, err := parquetgo.OpenFile(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	_, ok := pf.Schema().Lookup("pod", "namespace")
	return ok
}

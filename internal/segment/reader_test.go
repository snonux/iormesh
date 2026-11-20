package segment

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"iormesh/internal/schema"
)

// readAll streams an events file through ReadEvents, copying each
// borrowed event (the reader reuses its batch).
func readAll(t *testing.T, path string) ([]schema.Event, Meta, error) {
	t.Helper()
	var evs []schema.Event
	m, err := ReadEvents(context.Background(), path, func(ev *schema.Event) error {
		evs = append(evs, *ev)
		return nil
	})
	return evs, m, err
}

// manyEvents is n burst reads after the session header: enough rows for
// several reader batches and row groups.
func manyEvents(n int) []schema.Event {
	evs := []schema.Event{hostEvent(1, schema.KindSession)}
	for i := range n {
		evs = append(evs, burstEvent(uint64(i+2)))
	}
	return append(evs, richEvents()[1:]...)
}

// TestReadEventsRoundTrip writes events with the Writer (its own
// encoder) and reads them back with ReadEvents: the same NDJSON, and a
// footer that names the segment.
func TestReadEventsRoundTrip(t *testing.T) {
	for _, n := range []int{0, readBatch - 1, readBatch, 2*eventRowsPerRowGroup + 7} {
		w, dir := newTestWriter(t, nil)
		want := manyEvents(n)
		for i := range want {
			want[i].Seq = uint64(i + 1)
		}
		writeAll(t, w, want)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, ReadyDir, Name("app-1.example.org", "a1b2c3d4", t0, 0))
		got, m, err := readAll(t, path)
		if err != nil {
			t.Fatal(err)
		}
		sameNDJSON(t, got, want)
		wantMeta := Meta{SchemaVersion: schema.Version, Project: "shop", FQDN: "app-1.example.org",
			Session: "a1b2c3d4", Seq: 0, Start: t0, Content: ContentEvents}
		if m != wantMeta {
			t.Errorf("meta = %+v, want %+v", m, wantMeta)
		}
	}
}

// TestReadTableRoundTrip reads a methods table back through ReadTable.
func TestReadTableRoundTrip(t *testing.T) {
	w, dir := newTestWriter(t, nil)
	m, err := schema.ParseFrame("demo.Main#run(Main.java:12)")
	if err != nil {
		t.Fatal(err)
	}
	m.SchemaVersion, m.MethodID = schema.Version, 1
	if err := w.Method(m); err != nil {
		t.Fatal(err)
	}
	writeAll(t, w, richEvents())
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ReadyDir, TableName("app-1.example.org", "a1b2c3d4", t0, 0, TableMethods))
	var got []schema.Method
	meta, err := ReadTable(context.Background(), path, TableMethods, func(r *schema.Method) error {
		got = append(got, *r)
		return nil
	})
	if err != nil || meta.Content != TableMethods || len(got) != 1 || got[0].Frame != m.Frame {
		t.Fatalf("ReadTable = %+v, %+v, %v", got, meta, err)
	}
	// An events file is not a methods table, nor the reverse.
	if _, err := ReadTable(context.Background(), path, TableThreads,
		func(*schema.Thread) error { return nil }); err == nil {
		t.Error("a methods file read as threads")
	}
	if _, _, err := readAll(t, path); err == nil || !strings.Contains(err.Error(), `footer says "methods"`) {
		t.Errorf("a methods file read as events: %v", err)
	}
}

func TestParseName(t *testing.T) {
	start := time.Date(2026, 11, 20, 6, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		want FileName
		ok   bool
	}{
		{Name("app-1.example.org", "a1b2c3d4", start, 3),
			FileName{FQDN: "app-1.example.org", Session: "a1b2c3d4", Start: start, Seq: 3}, true},
		{TableName("h", "00ff00ff", start, 1234567, TableStacks),
			FileName{FQDN: "h", Session: "00ff00ff", Start: start, Seq: 1234567, Table: TableStacks}, true},
		{"h-a1b2c3d4-20261120T063000Z-000001.other.parquet", FileName{}, false},
		{"h-a1b2c3d4-20261120T063000Z-01.parquet", FileName{}, false},
		{"h-a1b2c3d4-20261120T063000Z-000001.parquet.tmp", FileName{}, false},
		{".h-a1b2c3d4-20261120T063000Z-000001.parquet.Xy12", FileName{}, false},
		{"a1b2c3d4-20261120T063000Z-000001.parquet", FileName{}, false},
		{"h-a1b2c3d4-20261320T063000Z-000001.parquet", FileName{}, false},
	} {
		got, err := ParseName(tc.name)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("ParseName(%q) = %+v, %v; want %+v ok=%v", tc.name, got, err, tc.want, tc.ok)
		}
		if !tc.ok {
			continue
		}
		rebuilt := got.Base()
		if got.Table != "" {
			rebuilt += "." + got.Table
		}
		if rebuilt+Ext != tc.name {
			t.Errorf("Base of %q = %q", tc.name, got.Base())
		}
	}
}

// TestReadDamagedFile: a truncated or overwritten events file is an
// error naming it, never a panic or a silent short read.
func TestReadDamagedFile(t *testing.T) {
	w, dir := newTestWriter(t, nil)
	writeAll(t, w, manyEvents(3000))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	good, err := os.ReadFile(filepath.Join(dir, ReadyDir, Name("app-1.example.org", "a1b2c3d4", t0, 0)))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"empty":     nil,
		"garbage":   []byte("not a parquet file at all"),
		"truncated": good[:len(good)/2],
		"no footer": good[:len(good)-8],
		"zeroed":    zeroed(good, 64, len(good)/3),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "x.parquet")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := readAll(t, path); err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("read of a %s file: %v", name, err)
			}
		})
	}
}

// zeroed returns a copy of data with [from, to) zeroed: pages destroyed,
// the footer intact.
func zeroed(data []byte, from, to int) []byte {
	out := append([]byte(nil), data...)
	clear(out[from:to])
	return out
}

// TestReadEventsStopsOnCallerError: fn's error comes back as is.
func TestReadEventsStopsOnCallerError(t *testing.T) {
	w, dir := newTestWriter(t, nil)
	writeAll(t, w, richEvents())
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("stop")
	n := 0
	_, err := ReadEvents(context.Background(), filepath.Join(dir, ReadyDir,
		Name("app-1.example.org", "a1b2c3d4", t0, 0)), func(*schema.Event) error {
		n++
		return stop
	})
	if !errors.Is(err, stop) || n != 1 {
		t.Fatalf("err = %v after %d events", err, n)
	}
}

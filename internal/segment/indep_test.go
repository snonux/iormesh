package segment

import (
	"bytes"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"iormesh/internal/schema"
)

// TestIndependentReaders reads events files with pyarrow and DuckDB, two
// parquet readers that share no code with parquet-go, and compares every
// value with the NDJSON of the same events (testdata/pqcheck.py; task
// ee4): the files are meant for any reader (docs/schema.md), and fields
// parquet-go's reader ignores — a chunk's num_values, null counts, the
// encodings list, sizes — are ones Arrow and DuckDB act on. Opt-in, as
// the readers are not a build dependency: IORMESH_PARQUET_CHECK names a
// Python venv with both installed (python3 -m venv DIR && DIR/bin/pip
// install pyarrow duckdb).
func TestIndependentReaders(t *testing.T) {
	venv := os.Getenv("IORMESH_PARQUET_CHECK")
	if venv == "" {
		t.Skip("IORMESH_PARQUET_CHECK (a venv with pyarrow and duckdb) not set")
	}
	r := rand.New(rand.NewPCG(7, 8))
	mixed := richEvents()
	for i := range 2*eventRowsPerRowGroup + 500 {
		mixed = append(mixed, randomEvent(r, uint64(i+100)))
	}
	// Burst reads only: every args, java, http, session, otel and dns
	// column all null, in every row group.
	var burst []schema.Event
	for i := range eventRowsPerRowGroup + 10 {
		burst = append(burst, burstEvent(uint64(i+1)))
	}
	for name, evs := range map[string][]schema.Event{"mixed": mixed, "burst": burst} {
		t.Run(name, func(t *testing.T) {
			path, ndjson := writeEventFile(t, evs), writeNDJSON(t, evs)
			out, err := exec.Command(filepath.Join(venv, "bin", "python"), "testdata/pqcheck.py",
				path, ndjson).CombinedOutput()
			t.Logf("%s", out)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// writeEventFile writes evs as one published events file and returns
// its path.
func writeEventFile(t *testing.T, evs []schema.Event) string {
	t.Helper()
	tmpl, err := newFooterTemplate()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s, err := openEventStream(filepath.Join(dir, "active"), newZstdCodec(), tmpl)
	if err != nil {
		t.Fatal(err)
	}
	for i := range evs {
		if err := s.add(&evs[i]); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "events.parquet")
	if err := s.publish(path, map[string]string{"iormesh.session": "a1b2c3d4"}); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeNDJSON(t *testing.T, evs []schema.Event) string {
	t.Helper()
	var b bytes.Buffer
	for i := range evs {
		b.Write(schema.AppendEvent(nil, &evs[i]))
		b.WriteByte('\n')
	}
	path := filepath.Join(t.TempDir(), "events.ndjson")
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

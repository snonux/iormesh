package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"iormesh/internal/agentspool"
	"iormesh/internal/correlate"
	"iormesh/internal/schema"
)

// TestOpenJavaWithoutSpool: no -spool removes stale tables and leaves a
// nil output whose methods do nothing.
func TestOpenJavaWithoutSpool(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{MethodsFile, ThreadsFile, ClassesFile} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	o, err := openJava(runConfig{out: dir}, nil, &bytes.Buffer{})
	if err != nil || o != nil {
		t.Fatalf("openJava = %v, %v", o, err)
	}
	for _, n := range []string{MethodsFile, ThreadsFile, ClassesFile} {
		if _, err := os.Stat(filepath.Join(dir, n)); !os.IsNotExist(err) {
			t.Errorf("stale %s kept: %v", n, err)
		}
	}
	sink := &bytes.Buffer{}
	next := schema.NewNDJSONSink(sink)
	if o.sink(next) != schema.Sink(next) || o.poll() != nil || o.close() != nil || o.summary() != "" {
		t.Error("nil javaOutput is not a no-op")
	}
}

// TestJavaOutputRun: with -spool the spool gets its mode, the joiner sits
// in front of the events file, a planted file is reported once, and the
// tables and summary come out at the end.
func TestJavaOutputRun(t *testing.T) {
	dir, spool := t.TempDir(), filepath.Join(t.TempDir(), "spool")
	var stderr bytes.Buffer
	o, err := openJava(runConfig{out: dir, spool: spool}, &fakeTracer{}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(spool); err != nil || st.Mode()&(os.ModePerm|os.ModeSticky) != agentspool.SpoolMode {
		t.Fatalf("spool %v, %v", st, err)
	}
	// A planted symlink: never mapped, reported once.
	if err := os.Symlink(filepath.Join(dir, EventsFile), filepath.Join(spool, "1.ring")); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, EventsFile))
	if err != nil {
		t.Fatal(err)
	}
	c := correlate.New(correlate.Config{Host: correlate.Host{ID: "h", Project: "p"},
		Sink: o.sink(schema.NewNDJSONSink(f)), ProcInfo: func(uint32) (string, uint64) { return "x", 1 }})
	if err := c.Session(schema.Session{BootID: "b", FQDN: "h", Kernel: "6", Arch: "x86_64"}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := o.poll(); err != nil {
			t.Fatal(err)
		}
	}
	if err := firstErr(c.End(schema.EndStop), c.Close(), o.close()); err != nil {
		t.Fatal(err)
	}
	if got := kinds(readEvents(t, dir)); got != "session,end" {
		t.Errorf("events %s", got)
	}
	if n := strings.Count(stderr.String(), "skipped agent ring"); n != 1 {
		t.Errorf("stderr %q: want the planted ring reported once", stderr.String())
	}
	if s := o.summary(); !strings.Contains(s, "java rings 0 (skipped 1, corrupt 0, unmarked JVMs 0)") {
		t.Errorf("summary %q", s)
	}
	for _, n := range []string{MethodsFile, ThreadsFile, ClassesFile} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
}

// firstErr returns the first non-nil error.
func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

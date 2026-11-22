package k8s

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"testing"
)

// errSync is the failure syncFS injects.
var errSync = errors.New("injected sync failure")

// syncFS is the real rootFS with every operation logged in order, and a
// file or directory sync that fails on demand (task ii4).
type syncFS struct {
	rootFS
	log         *[]string
	failFile    string // the temporary whose Sync fails
	failDirSync bool
}

func (s syncFS) Create(name string) (unpackFile, error) {
	f, err := s.rootFS.Create(name)
	if err != nil {
		return nil, err
	}
	return syncFile{unpackFile: f, name: name, fs: s}, nil
}

func (s syncFS) Rename(oldname, newname string) error {
	*s.log = append(*s.log, "rename "+newname)
	return s.rootFS.Rename(oldname, newname)
}

func (s syncFS) SyncDir() error {
	*s.log = append(*s.log, "syncdir")
	if s.failDirSync {
		return errSync
	}
	return s.rootFS.SyncDir()
}

// syncFile logs its Sync under the temporary's name.
type syncFile struct {
	unpackFile
	name string
	fs   syncFS
}

func (f syncFile) Sync() error {
	*f.fs.log = append(*f.fs.log, "sync "+f.name)
	if f.name == f.fs.failFile {
		return errSync
	}
	return f.unpackFile.Sync()
}

// injectSyncFS makes Unpack write through a syncFS configured by cfg and
// returns its log.
func injectSyncFS(t *testing.T, cfg syncFS) *[]string {
	t.Helper()
	var log []string
	old := newUnpackFS
	newUnpackFS = func(root *os.Root) unpackFS {
		cfg.rootFS, cfg.log = rootFS{root}, &log
		return cfg
	}
	t.Cleanup(func() { newUnpackFS = old })
	return &log
}

// twoSegments is a stream of a.parquet and b.parquet.
func twoSegments(t *testing.T) []byte {
	return hostileTar(t,
		&tar.Header{Typeflag: tar.TypeReg, Name: "a.parquet", Size: 4},
		&tar.Header{Typeflag: tar.TypeReg, Name: "b.parquet", Size: 4})
}

// TestUnpackSyncsBeforeRename: each temporary is fsynced before it is
// renamed into place, and the directory once after the last rename, so
// what Unpack returns survives a crash.
func TestUnpackSyncsBeforeRename(t *testing.T) {
	log := injectSyncFS(t, syncFS{})
	taken, err := Unpack(bytes.NewReader(twoSegments(t)), t.TempDir())
	if err != nil || !slices.Equal(taken, []string{"a.parquet", "b.parquet"}) {
		t.Fatalf("Unpack = %q, %v", taken, err)
	}
	want := []string{"sync .a.parquet.part", "rename a.parquet",
		"sync .b.parquet.part", "rename b.parquet", "syncdir"}
	if !slices.Equal(*log, want) {
		t.Errorf("order\n got %q\nwant %q", *log, want)
	}
}

// TestUnpackFileSyncFailure: a segment whose sync fails is not renamed
// into place nor taken, and leaves no temporary; the one before it is
// still taken, after the directory sync.
func TestUnpackFileSyncFailure(t *testing.T) {
	log := injectSyncFS(t, syncFS{failFile: ".b.parquet.part"})
	out := t.TempDir()
	taken, err := Unpack(bytes.NewReader(twoSegments(t)), out)
	if !errors.Is(err, errSync) || !slices.Equal(taken, []string{"a.parquet"}) {
		t.Fatalf("Unpack = %q, %v; want a.parquet and the sync error", taken, err)
	}
	if slices.Contains(*log, "rename b.parquet") || (*log)[len(*log)-1] != "syncdir" {
		t.Errorf("log = %q", *log)
	}
	assertDir(t, out, map[string]string{"a.parquet": "xxxx"})
}

// TestFetchSyncFailureKeepsTracerCopy: when a sync fails, Fetch never
// reaches removeTaken for what is not durable — the tracer keeps its
// copy for the next collect. A failed directory sync removes nothing at
// all; a failed file sync removes only the segments before it.
func TestFetchSyncFailureKeepsTracerCopy(t *testing.T) {
	cases := map[string]struct {
		cfg     syncFS
		removed []string
	}{
		"directory sync": {cfg: syncFS{failDirSync: true}},
		"file sync":      {cfg: syncFS{failFile: ".b.parquet.part"}, removed: []string{"a.parquet"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.tar(t, twoSegments(t))
			injectSyncFS(t, tc.cfg)
			err := newTestKubectl(t, testTarget()).Fetch(context.Background(),
				"/opt/iormesh/data/traces/shop/ready", t.TempDir())
			if !errors.Is(err, errSync) {
				t.Fatalf("err = %v, want the sync error", err)
			}
			calls := f.calls(t)
			if tc.removed == nil {
				if len(calls) != 1 {
					t.Errorf("removeTaken ran: calls = %q", calls)
				}
				return
			}
			if len(calls) != 2 || !slices.Equal(calls[1][len(calls[1])-len(tc.removed)-1:],
				slices.Concat([]string{"--"}, tc.removed)) {
				t.Errorf("calls = %q, want a remove of %q", calls, tc.removed)
			}
		})
	}
}

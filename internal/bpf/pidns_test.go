package bpf

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

// fakeProc builds a /proc whose self links to the directory self, holding
// ns/pid as a plain file: an inode that is not the initial namespace's.
func fakeProc(t *testing.T, self string) string {
	t.Helper()
	proc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proc, self, "ns"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proc, self, "ns", "pid"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, filepath.Join(proc, "self")); err != nil {
		t.Fatal(err)
	}
	return proc
}

// TestTracerPidNSRefusesForeignProc: in a non-initial namespace, a /proc
// whose self names another pid (the host's procfs) is refused.
func TestTracerPidNSRefusesForeignProc(t *testing.T) {
	proc := fakeProc(t, "999999999")
	if _, _, err := tracerPidNS(proc); !errors.Is(err, errForeignProc) {
		t.Fatalf("tracerPidNS = %v, want errForeignProc", err)
	}
}

// TestTracerPidNSOwnProc: a /proc whose self is the tracer's pid yields the
// namespace's inode.
func TestTracerPidNSOwnProc(t *testing.T) {
	proc := fakeProc(t, strconv.Itoa(os.Getpid()))
	inum, _, err := tracerPidNS(proc)
	if err != nil || inum == 0 {
		t.Fatalf("tracerPidNS = %d, %v; want the fake namespace's inode", inum, err)
	}
}

// TestTracerPidNSHost: on the test host (the initial namespace, as CI and
// `mage test` run) the real /proc gives 0, host mode. Skipped elsewhere.
func TestTracerPidNSHost(t *testing.T) {
	inum, _, err := tracerPidNS("/proc")
	if err != nil {
		t.Fatal(err)
	}
	if inum != 0 {
		t.Skipf("running in PID namespace %d, not the initial one", inum)
	}
}

// TestProbesForPidNSSwapsNewtask: namespace mode attaches the raw
// task_newtask handler in place of the classic one and nothing else
// changes; host mode keeps the probes as they are.
func TestProbesForPidNSSwapsNewtask(t *testing.T) {
	probes := DefaultProbes()
	if got := probesForPidNS(probes, false); !slices.Equal(got, probes) {
		t.Fatal("host mode changed the probes")
	}
	got := probesForPidNS(probes, true)
	if len(got) != len(probes) {
		t.Fatalf("%d probes in namespace mode, want %d", len(got), len(probes))
	}
	swapped := 0
	for i, p := range got {
		switch {
		case probes[i].Program == newtaskProg:
			if p.Program != newtaskNSProg || p.Category != rawCategory || p.Event != "task_newtask" {
				t.Errorf("newtask probe in namespace mode: %+v", p)
			}
			swapped++
		case p != probes[i]:
			t.Errorf("probe %d changed: %+v -> %+v", i, probes[i], p)
		}
	}
	if swapped != 1 {
		t.Fatalf("swapped %d newtask probes, want 1", swapped)
	}
}

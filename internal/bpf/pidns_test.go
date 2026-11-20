package bpf

import (
	"errors"
	"os"
	"path/filepath"
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

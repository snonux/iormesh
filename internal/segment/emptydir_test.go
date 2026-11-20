package segment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTraceRootInsideWorldWritableVolume is the k8s layout (M7, task
// vb4; k8s.TraceRoot): the kubelet makes a pod emptyDir's root 0777 (with
// setgid under fsGroup), so the tracer's trace dir is a directory one
// level down that MakeDirs creates 0755. prepareDir must accept that —
// it checks the trace dir and the project directory, not the volume above
// them — and must still refuse the volume root itself as the trace dir.
func TestTraceRootInsideWorldWritableVolume(t *testing.T) {
	vol := filepath.Join(t.TempDir(), "volume")
	if err := os.Mkdir(vol, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(vol, 0o777|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(vol, "traces")
	if err := prepareDir(filepath.Join(root, "shop"), nil); err != nil {
		t.Fatalf("trace dir inside a 0777 volume: %v", err)
	}
	fi, err := os.Stat(root)
	if err != nil || fi.Mode().Perm() != 0o755 || fi.Mode()&os.ModeSetgid != 0 {
		t.Fatalf("trace root %v, %v; want a fresh 0755 directory", fi.Mode(), err)
	}
	// The volume root as the trace dir is still refused: trustedDir is
	// not relaxed for the k8s layout.
	err = prepareDir(filepath.Join(vol, "shop"), nil)
	if err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("volume root as trace dir: %v, want a writable refusal", err)
	}
}

// TestWatchdogDirBytes: the byte cap on the trace dir itself trips at
// the cap, counts only regular files, and is off at 0.
func TestWatchdogDirBytes(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "p", ReadyDir), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, n := range map[string]int{"a.parquet": 600, "p/ready/b.parquet": 400} {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/dev/zero", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if n, err := DirBytes(dir); err != nil || n != 1000 {
		t.Fatalf("DirBytes %d, %v; want 1000", n, err)
	}
	room := func(string) (Usage, error) { return Usage{Used: 1, Avail: 1 << 40}, nil }
	for limit, trips := range map[uint64]bool{0: false, 1001: false, 1000: true, 999: true} {
		err := Watchdog{Dir: dir, MaxDirBytes: limit, Statfs: room}.Check()
		if (err != nil) != trips {
			t.Errorf("cap %d: %v, want tripped %v", limit, err, trips)
		}
	}
}

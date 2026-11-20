package acceptance

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"iormesh/internal/federate"
)

// TestInterruptStillTearsDown: a run cancelled mid-way (^C during the
// streaming rounds) still removes both hosts, stops leftover workload
// roles and deletes its run directory on each server.
func TestInterruptStillTearsDown(t *testing.T) {
	cfg := testConfig(t)
	w := newFakeWorld(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out bytes.Buffer
	r := New(cfg, Deps{Ctl: fakeCtl{w}, Out: &out,
		Dial: func(h string) (federate.Transport, error) { return fakeTransport{w, h}, nil },
		Sleep: func(context.Context, time.Duration) error {
			cancel() // ^C while waiting for the first rotation
			return context.Canceled
		}})
	_ = r.Run(ctx)
	if !w.removed["h1"] || !w.removed["h2"] {
		t.Errorf("rm after the interrupt: %v\n%s", w.removed, out.String())
	}
	for _, h := range []string{"h1", "h2"} {
		for _, want := range []string{h + ": pkill -f /home/op/.iormesh-m4-run/workload.jar",
			h + ": rm -rf -- /home/op/.iormesh-m4-run"} {
			if !slices.Contains(w.remote, want) {
				t.Errorf("missing cleanup %q", want)
			}
		}
	}
}

// TestFailedClientStopsServer: when a two-host client fails, its server
// on HOST2 is killed before the run waits for it (it would otherwise sit
// in accept() until the role timeout), and the criterion reports the
// workload's failure.
func TestFailedClientStopsServer(t *testing.T) {
	cfg := testConfig(t)
	w := newFakeWorld(t, cfg)
	w.failClient = "tcp-two-host"
	out, _ := dryRun(t, cfg, w)
	kill := "h2: pkill -f /home/op/.iormesh-m4-run/workload.jar"
	i := slices.Index(w.remote, kill)
	if i < 0 {
		t.Fatalf("no %q after the failed client\n%s", kill, out)
	}
	if !hasResult(out, cCross, Fail) || !strings.Contains(out, "workload:") {
		t.Errorf("M4-6 does not report the workload failure\n%s", out)
	}
}

// TestPreflightRefusesController: a host with the controller's own
// machine id is refused before anything is deployed.
func TestPreflightRefusesController(t *testing.T) {
	cfg := testConfig(t)
	w := newFakeWorld(t, cfg)
	id := filepath.Join(t.TempDir(), "machine-id")
	if err := os.WriteFile(id, []byte("machine-h2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := localMachineID
	localMachineID = id
	defer func() { localMachineID = old }()
	out, _ := dryRun(t, cfg, w)
	if !hasResult(out, cPreflight, Fail) || !strings.Contains(out, "h2 is this controller") ||
		len(w.ctlCalls) != 0 {
		t.Errorf("controller not refused (ctl calls %v)\n%s", w.ctlCalls, out)
	}
}

// TestNewExecCtlEnv: iormeshctl gets exactly one HOME (the work dir), its
// config under it, no inherited XDG_DATA_HOME, and on Linux the
// operator's cache dir for the ssh control sockets.
func TestNewExecCtlEnv(t *testing.T) {
	t.Setenv("HOME", "/orig")
	t.Setenv("XDG_CONFIG_HOME", "/orig/cfg")
	t.Setenv("XDG_DATA_HOME", "/orig/data")
	t.Setenv("XDG_CACHE_HOME", "/orig/cache")
	c, err := NewExecCtl("/bin/iormeshctl", "/work")
	if err != nil {
		t.Fatal(err)
	}
	env := c.(execCtl).env
	got := map[string][]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = append(got[k], v)
	}
	want := map[string][]string{"HOME": {"/work"}, "XDG_CONFIG_HOME": {"/work/.config"}}
	if runtime.GOOS != "darwin" {
		want["XDG_CACHE_HOME"] = []string{"/orig/cache"}
	}
	for k, v := range want {
		if !slices.Equal(got[k], v) {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if len(got["XDG_DATA_HOME"]) != 0 {
		t.Errorf("XDG_DATA_HOME inherited: %v", got["XDG_DATA_HOME"])
	}
}

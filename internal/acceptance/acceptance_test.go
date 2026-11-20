package acceptance

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"iormesh/internal/federate"
)

// testConfig is a full config on two fake hosts, with local inputs that
// exist (the preflight stats them).
func testConfig(t *testing.T) Config {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"workload.jar", "iormeshctl", "minisign.pub",
		"dist/iormesh-linux-amd64", "dist/iormesh-linux-amd64.minisig"} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := map[string]string{EnvNATPort: "18880", EnvDist: filepath.Join(dir, "dist"),
		EnvKey: filepath.Join(dir, "minisign.pub"), EnvWorkDir: filepath.Join(dir, "work")}
	cfg, err := FromEnv("h1", "h2", dir, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	cfg.Jar, cfg.Ctl = filepath.Join(dir, "workload.jar"), filepath.Join(dir, "iormeshctl")
	cfg.Goldens = filepath.Join("..", "..", "workload", "scenarios")
	return cfg
}

// dryRun runs the whole sequence against the fake world.
func dryRun(t *testing.T, cfg Config, w *fakeWorld) (string, error) {
	t.Helper()
	var out bytes.Buffer
	r := New(cfg, Deps{Ctl: fakeCtl{w}, Out: &out,
		Dial:  func(h string) (federate.Transport, error) { return fakeTransport{w, h}, nil },
		Sleep: func(context.Context, time.Duration) error { return nil }})
	err := r.Run(context.Background())
	return out.String(), err
}

// TestDryRun drives every phase against the fake hosts and iormeshctl:
// each criterion the fake can satisfy passes, the single-host golden
// fails on the fake's empty trace (the check is not vacuous), M4-9 is
// reported NOT RUN, and the verdict is "not accepted".
func TestDryRun(t *testing.T) {
	cfg := testConfig(t)
	w := newFakeWorld(t, cfg)
	out, err := dryRun(t, cfg, w)
	if !errors.Is(err, ErrNotAccepted) {
		t.Fatalf("Run = %v, want ErrNotAccepted\n%s", err, out)
	}
	want := map[string]Status{cPreflight: Pass, cDeploy: Pass, cStream: Pass, cCollect: Pass,
		cRefresh: Pass, cSingle: Fail, cCross: Pass, cNAT: Pass, cMicro: Pass, cMicroHost: NotRun,
		cClock: Pass, cOffline: Pass, cNaming: Pass, cWatchdog: Pass, cRm: Pass}
	for id, st := range want {
		if !hasResult(out, id, st) {
			t.Errorf("%s: want %s\n%s", id, st, out)
		}
	}
	if !strings.Contains(out, "VERDICT: not accepted") {
		t.Errorf("no verdict line\n%s", out)
	}
}

// hasResult finds the result line "<STATUS> <id> ..." in the report.
func hasResult(out, id string, st Status) bool {
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(strings.TrimPrefix(l, string(st)))
		if strings.HasPrefix(l, string(st)+" ") && len(f) > 0 && f[0] == id {
			return true
		}
	}
	return false
}

// TestDryRunTouchesOnlyTheTwoHosts: every remote command went to h1 or
// h2, the tracer's lifecycle only through iormeshctl (no remote start,
// stop, upgrade or uninstall), and both hosts were removed at the end.
func TestDryRunTouchesOnlyTheTwoHosts(t *testing.T) {
	cfg := testConfig(t)
	w := newFakeWorld(t, cfg)
	if _, err := dryRun(t, cfg, w); !errors.Is(err, ErrNotAccepted) {
		t.Fatal(err)
	}
	for _, c := range w.remote {
		host, argv, _ := strings.Cut(c, ": ")
		if host != "h1" && host != "h2" {
			t.Errorf("remote command on %q: %s", host, argv)
		}
		for _, verb := range []string{" start", " stop", " upgrade", " uninstall", " run "} {
			if strings.HasPrefix(argv, "sudo") && strings.Contains(argv, verb) {
				t.Errorf("runner drove the tracer itself: %s", c)
			}
		}
	}
	if !w.removed["h1"] || !w.removed["h2"] {
		t.Errorf("rm not run for both hosts: %v", w.removed)
	}
}

// TestPreflightRefusesBusyHost: a host with a trace directory is
// refused before anything is added or deployed.
func TestPreflightRefusesBusyHost(t *testing.T) {
	cfg := testConfig(t)
	w := newFakeWorld(t, cfg)
	busy := &busyWorld{w}
	var out bytes.Buffer
	r := New(cfg, Deps{Ctl: fakeCtl{w}, Out: &out,
		Dial:  func(h string) (federate.Transport, error) { return busyTransport{busy, h}, nil },
		Sleep: func(context.Context, time.Duration) error { return nil }})
	if err := r.Run(context.Background()); !errors.Is(err, ErrNotAccepted) {
		t.Fatal(err)
	}
	if !hasResult(out.String(), cPreflight, Fail) || len(w.ctlCalls) != 0 {
		t.Errorf("preflight passed or iormeshctl ran: %v\n%s", w.ctlCalls, out.String())
	}
	if !strings.Contains(out.String(), "is not empty") {
		t.Errorf("no reason given\n%s", out.String())
	}
}

// busyWorld is the fake with traces already under /opt/iormesh/traces.
type busyWorld struct{ *fakeWorld }

type busyTransport struct {
	w    *busyWorld
	host string
}

func (b busyTransport) Run(ctx context.Context, argv ...string) ([]byte, error) {
	if len(argv) == 3 && argv[0] == "test" && argv[2] == traceRoot {
		return nil, nil
	}
	if argv[0] == "ls" && argv[len(argv)-1] == traceRoot {
		return []byte("someones-project\n"), nil
	}
	return fakeTransport{b.w.fakeWorld, b.host}.Run(ctx, argv...)
}

func (b busyTransport) Fetch(context.Context, string, string) error   { return nil }
func (b busyTransport) Push(context.Context, string, ...string) error { return nil }

// TestHostGateRefusesOthers: no third host gets a transport.
func TestHostGateRefusesOthers(t *testing.T) {
	dialed := 0
	g := newHostGate("h1", "u@h2", func(string) (federate.Transport, error) {
		dialed++
		return nil, nil
	})
	for _, h := range []string{"h3", "h2", "u@h1", ""} {
		if _, err := g.get(h); !errors.Is(err, ErrForeignHost) {
			t.Errorf("get(%q) = %v, want ErrForeignHost", h, err)
		}
	}
	if _, err := g.get("u@h2"); err != nil || dialed != 1 {
		t.Errorf("get(u@h2) = %v, dialed %d", err, dialed)
	}
}

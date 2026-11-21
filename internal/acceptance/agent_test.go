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

// wantAgentProjects maps each microservices JVM — host, role and --dir —
// to the project tracing it with the agent (M4-8 on HOST1, the M4-9
// spread over both hosts). Written out, not derived from projects(), so
// the test does not share the code it checks.
var wantAgentProjects = map[string]string{
	"h1 gateway microservices": "m4gw", "h1 orders microservices": "m4orders",
	"h1 inventory microservices": "m4inv", "h1 billing microservices": "m4bill",
	"h1 gateway microservices-spread": "m4sgw", "h1 inventory microservices-spread": "m4sinv",
	"h2 orders microservices-spread": "m4sord", "h2 billing microservices-spread": "m4sbill",
}

// TestDryRunMicroservicesWithAgent (task kf4): the microservices projects
// of M4-8 and M4-9 are added with their confined spool and the installed
// agent jar (so deploy ships the signed jar), every microservices JVM
// starts with that jar as -javaagent writing into its own project's
// spool, no other project or JVM gets the agent, and M4-8 and M4-9 pass
// on the fakes.
func TestDryRunMicroservicesWithAgent(t *testing.T) {
	cfg := testConfig(t)
	w := newFakeWorld(t, cfg)
	out, err := dryRun(t, cfg, w)
	if !errors.Is(err, ErrNotAccepted) {
		t.Fatalf("Run = %v\n%s", err, out)
	}
	for _, id := range []string{cMicro, cMicroHost} {
		if !hasResult(out, id, Pass) {
			t.Errorf("%s not passed\n%s", id, out)
		}
	}
	checkAgentAdds(t, w.ctlCalls)
	checkAgentLaunches(t, w.launched)
}

// checkAgentAdds requires -spool/-agent on exactly the agent projects'
// add calls.
func checkAgentAdds(t *testing.T, calls []string) {
	t.Helper()
	agents := map[string]bool{}
	for _, p := range wantAgentProjects {
		agents[p] = true
	}
	seen := 0
	for _, c := range calls {
		f := strings.Fields(c)
		if len(f) < 3 || f[0] != "add" {
			continue
		}
		proj := f[2]
		want := " -spool /run/iormesh/spool/" + proj + " -agent /opt/iormesh/lib/iormesh-agent.jar"
		switch has := strings.Contains(c, want); {
		case agents[proj] && !has:
			t.Errorf("agent project added without its spool and jar: %s", c)
		case !agents[proj] && (strings.Contains(c, "-agent") || strings.Contains(c, "-spool")):
			t.Errorf("project %s got the agent: %s", proj, c)
		case has:
			seen++
		}
	}
	if seen != len(agents) {
		t.Errorf("%d agent projects added, want %d: %q", seen, len(agents), calls)
	}
}

// checkAgentLaunches requires every microservices JVM to run with its
// project's -javaagent and every other JVM with none.
func checkAgentLaunches(t *testing.T, launched []launch) {
	t.Helper()
	micro := 0
	for _, l := range launched {
		f := strings.Fields(l.cmdline)
		if f[3] != "microservices" {
			if l.env != "" {
				t.Errorf("%s: non-agent JVM with %s: %s", l.host, l.env, l.cmdline)
			}
			continue
		}
		micro++
		key := l.host + " " + f[5] + " " + filepath.Base(f[len(f)-1])
		proj, ok := wantAgentProjects[key]
		want := "JAVA_TOOL_OPTIONS=-javaagent:/opt/iormesh/lib/iormesh-agent.jar=spool=/run/iormesh/spool/" + proj
		if !ok || l.env != want {
			t.Errorf("%s: env %q, want %q (%s)", key, l.env, want, l.cmdline)
		}
	}
	if micro != len(wantAgentProjects) {
		t.Errorf("%d microservices JVMs launched, want %d", micro, len(wantAgentProjects))
	}
}

// TestPreflightRefusesMissingAgentJar: without the signed agent jar in
// dist/ the run stops in the preflight, before iormeshctl adds or
// deploys anything.
func TestPreflightRefusesMissingAgentJar(t *testing.T) {
	for _, f := range []string{"iormesh-agent.jar", "iormesh-agent.jar.minisig"} {
		t.Run(f, func(t *testing.T) {
			cfg := testConfig(t)
			if err := os.Remove(filepath.Join(cfg.Dist, f)); err != nil {
				t.Fatal(err)
			}
			w := newFakeWorld(t, cfg)
			var out bytes.Buffer
			r := New(cfg, Deps{Ctl: fakeCtl{w}, Out: &out,
				Dial:  func(h string) (federate.Transport, error) { return fakeTransport{w, h}, nil },
				Sleep: func(context.Context, time.Duration) error { return nil }})
			if err := r.Run(context.Background()); !errors.Is(err, ErrNotAccepted) {
				t.Fatal(err)
			}
			if !hasResult(out.String(), cPreflight, Fail) || !strings.Contains(out.String(), "signed agent jar") ||
				len(w.ctlCalls) != 0 {
				t.Errorf("preflight passed or iormeshctl ran: %v\n%s", w.ctlCalls, out.String())
			}
		})
	}
}

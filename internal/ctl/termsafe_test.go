package ctl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"iormesh/internal/k8s"
	"iormesh/internal/routemap"
	"iormesh/internal/servicedb"
	"iormesh/internal/termsafe"
)

// escText is text a compromised host, cluster or file system could hand
// iormeshctl: an OSC that sets the window title and a CSI that clears the
// screen (task lj4).
const escText = "\x1b]0;pwned\a\x1b[2J"

// assertTerminalSafe fails t when out (one command's stdout or stderr)
// carries a byte a terminal could act on: every line must already be
// what termsafe.String leaves unchanged, so an escape sequence that got
// through raw instead of Go-quoted is caught whichever print let it in.
func assertTerminalSafe(t *testing.T, what, out string) {
	t.Helper()
	for line := range strings.Lines(out) {
		line = strings.TrimSuffix(line, "\n")
		if termsafe.String(line) != line {
			t.Errorf("%s carries a raw control byte: %q", what, line)
		}
	}
}

// TestHostileTextTerminalSafe drives each command that prints server-,
// cluster- or file-supplied text against its fake transport answering
// with escape sequences, and checks none reaches the terminal raw
// (assertTerminalSafe): pods deploy's pod labels (a selector's pod names
// come from the API server), deploy's verified lines (the -dist path),
// its relay of a host's output, and expand -apply's enrolled host's.
// Some printed fields cannot carry an escape this way (expand's addresses
// are netip.Addr, registry hosts and project names are validated, session
// ids are checked against their format in internal/k8s); the commands
// quote those too, as defence in depth, which this test cannot reach.
func TestHostileTextTerminalSafe(t *testing.T) {
	t.Run("pods deploy", func(t *testing.T) {
		// internal/k8s refuses a pod name that is no DNS label before
		// any deploy; the refusal itself must print it quoted.
		f := newPodFixture(t, "")
		name := "web-" + escText
		f.list(t, podObject(name, uid(2), false))
		f.pod(t, podObject(name, uid(2), false))
		if code := f.run("deploy", "kind/shop/app=web"); code != exitError ||
			!strings.Contains(f.stderr.String(), "name refused") {
			t.Fatalf("exit %d: %s", code, f.stderr)
		}
		assertTerminalSafe(t, "stdout", f.stdout.String())
		assertTerminalSafe(t, "stderr", f.stderr.String())
	})
	t.Run("deploy", func(t *testing.T) {
		f := agentFixture(t)
		dist := filepath.Join(t.TempDir(), "dist"+escText)
		if err := os.Rename(f.dist, dist); err != nil {
			t.Fatal(err)
		}
		f.answers["app1 sudo -n /opt/iormesh/bin/iormesh status"] = reply{out: "state " + escText + "\n"}
		if code := f.run("deploy", "-dist", dist, "app1"); code != exitOK {
			t.Fatalf("exit %d: %s", code, f.stderr)
		}
		if !strings.Contains(f.stdout.String(), "verified ") {
			t.Errorf("stdout: %q", f.stdout)
		}
		assertTerminalSafe(t, "stdout", f.stdout.String())
		assertTerminalSafe(t, "stderr", f.stderr.String())
	})
	t.Run("expand -apply", func(t *testing.T) {
		f := newApplyFixture(t, dbAllowed)
		f.answers["203.0.113.9 sudo -n /opt/iormesh/bin/iormesh status"] = reply{out: escText + "\n"}
		if code := f.apply(); code != exitOK {
			t.Fatalf("exit %d: %s\n%s", code, f.stderr, f.stdout)
		}
		assertTerminalSafe(t, "stdout", f.stdout.String())
		assertTerminalSafe(t, "stderr", f.stderr.String())
	})
}

// TestHostileValuesPrintedSafe (task oj4) feeds the printers of
// services, routes discover and pods collect values a compromised trace,
// cluster or service database could hold, past the validation that
// normally stops them before they are printed (servicedb refuses such a
// rule, internal/k8s such a pod name, the registry such a context): each
// print escapes its values itself, whatever validated them upstream.
// TestCtlPrintsAreTerminalSafe in internal/buildgate keeps every other
// print in this package doing the same.
func TestHostileValuesPrintedSafe(t *testing.T) {
	t.Run("services list", func(t *testing.T) {
		var b strings.Builder
		eps := []servicedb.Endpoint{{Service: "inv" + escText, IP: "10.0.0.2" + escText, Port: 8443,
			TLS: escText, Requests: []servicedb.EndpointRequest{{Method: "GET", Path: "/"}}}}
		if err := writeEndpoints(&b, eps, true); err != nil {
			t.Fatal(err)
		}
		assertTerminalSafe(t, "services list", b.String())
	})
	t.Run("routes discover", func(t *testing.T) {
		e, stdout, _, _ := testEnv(t)
		s := routemap.Snapshot{Context: "kind" + escText, Namespaces: []string{"prod" + escText},
			Flags: []routemap.Flag{{Kind: "shadowed" + escText, Subject: "s", Detail: "d"}}}
		if err := printDiscovery(e, s, "/snap"); err != nil {
			t.Fatal(err)
		}
		assertTerminalSafe(t, "routes discover", stdout.String())
	})
	t.Run("pods collect", func(t *testing.T) {
		e, stdout, _, _ := testEnv(t)
		pe := podEntry{ref: "kind/shop/" + escText}
		st := k8s.PodState{Name: "web-" + escText}
		if err := (collector{e: e}).pod(context.Background(), pe, st, nil); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stdout.String(), "nothing to collect") {
			t.Errorf("stdout: %q", stdout)
		}
		assertTerminalSafe(t, "pods collect", stdout.String())
	})
}

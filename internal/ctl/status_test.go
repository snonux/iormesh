package ctl

import (
	"errors"
	"strings"
	"testing"
)

// statusLine is the table row of target and project in out, "" when
// there is none; its cells are split on runs of spaces.
func statusLine(out, target, project string) []string {
	for line := range strings.Lines(out) {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == target && f[1] == project {
			return f
		}
	}
	return nil
}

// TestStatusHosts (task ph4): status asks each registered host once,
// through the sudoers rule's `status -json`, and prints a row per
// project: state, end reason, ends-by, the run's tracer version
// ("unknown" for a state.json from before the field) and the ready/
// segments awaiting collection. A registered project the host does not
// know is "none", a session the registry does not list is shown, a
// report's error is a failed row, and a host that cannot be reached is a
// failed row too — the others are still asked, and the command exits 1.
// The servers' text reaches the terminal escaped.
func TestStatusHosts(t *testing.T) {
	f := newDeployFixture(t)
	f.answers["app1 "+strings.Join(sudoTr, " ")+" status -json"] = reply{out: `[
	 {"project":"shop","unit_state":"active/running","state":"running","ready_segments":3,
	  "session":{"ends_by":"2026-11-22T14:00:00Z","version":"0.9.1"}},
	 {"project":"old","unit_state":"not loaded","state":"ended","ready_segments":0,
	  "session":{"ends_by":"2026-11-22T09:00:00Z","end_reason":"duration"}},
	 {"project":"evil","unit_state":"x","state":"ended","ready_segments":0,
	  "session":{"end_reason":"\u001b[2J","version":"1\u001b]0;x\u0007"}}]`}
	f.answers["app2 "] = reply{err: errors.New("ssh app2: connection refused")}
	if code := f.run("status"); code != exitError {
		t.Fatalf("exit %d, want 1: %s", code, f.stderr)
	}
	checkLog(t, f.log, [][]string{sudoCall("app1", "status", "-json")}, [][]string{sudoCall("app2", "status", "-json")})
	if !strings.Contains(f.stderr.String(), "status failed on 1 of 2 targets") {
		t.Errorf("stderr %q", f.stderr)
	}
	// A report carrying an error (a corrupt state.json) is a failed row;
	// status -json then exits 1, but its reports are still read.
	f.answers["app2 "] = reply{out: `[{"project":"shop","unit_state":"x","error":"state: corrupt"}]`,
		err: exitStatus(t, 1)}
	code := f.run("status", "app2")
	if row := statusLine(f.stdout.String(), "app2", "shop"); code != exitError || len(row) < 4 ||
		strings.Join(row[2:4], " ") != "error -" ||
		!strings.Contains(f.stdout.String(), "state: corrupt") {
		t.Errorf("failed report: exit %d:\n%s", code, f.stdout)
	}
	f.answers["app2 "] = reply{err: errors.New("ssh app2: connection refused")}
	f.run("status")
	out := f.stdout.String()
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Errorf("raw control bytes reached the terminal:\n%q", out)
	}
	for _, c := range []struct {
		target, project string
		want            []string
	}{
		{"app1", "shop", []string{"running", "-", "2026-11-22T14:00:00Z", "0.9.1", "3"}},
		{"app1", "billing", []string{"none", "-", "-", "-", "-"}},
		{"app1", "old", []string{"ended", "duration", "-", "unknown", "0"}},
		{"app2", "shop", []string{"error", "-", "-", "-", "-"}},
	} {
		got := statusLine(out, c.target, c.project)
		if len(got) < 7 || strings.Join(got[2:7], " ") != strings.Join(c.want, " ") {
			t.Errorf("%s %s: %q, want %q in\n%s", c.target, c.project, got, c.want, out)
		}
	}
	if !strings.Contains(out, "not in the registry") || !strings.Contains(out, "connection refused") {
		t.Errorf("notes missing:\n%s", out)
	}
	if !strings.Contains(f.stderr.String(), "status failed on 1 of 2 targets") {
		t.Errorf("stderr %q", f.stderr)
	}
	// Narrowed to one host and project: only it is asked, and a healthy
	// answer exits 0; an unregistered host is refused before any contact.
	if code := f.run("status", "-project", "shop", "app1"); code != exitOK || statusLine(f.stdout.String(), "app1", "billing") != nil {
		t.Errorf("narrowed: exit %d:\n%s%s", code, f.stdout, f.stderr)
	}
	if code := f.run("status", "nope"); code != exitError || len(f.log) != 0 {
		t.Errorf("unregistered host: exit %d, calls %q", code, f.log)
	}
}

// TestStatusPods (task ph4): a pod's tracer is asked `iormesh ready
// -status` per project through the kubectl transport; an image from
// before the flag answers `ready -state` instead, its version and ready/
// count then unknown; a pod without a tracer container is "none"; a pod
// gone is a failed row, never an abort.
func TestStatusPods(t *testing.T) {
	f := newPodFixture(t, "")
	f.pod(t, podObject("checkout-0", uid(1), true))
	f.file(t, "status-checkout-0", []byte(`{"project":"shop","unit":"","unit_state":"","state":"running",`+
		`"session":{"ends_by":"2026-11-22T14:00:00Z","version":"0.9.1"},"ready_segments":2}`))
	f.list(t, podObject("web-1", uid(2), true), podObject("web-2", uid(3), false))
	if code := f.run("status"); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr)
	}
	out := f.stdout.String()
	// A pod's target cell is "<entry> pod <name>": three fields.
	for _, c := range []struct {
		target string
		want   []string
	}{
		{"kind/shop/checkout-0 pod checkout-0", []string{"shop", "running", "-", "2026-11-22T14:00:00Z", "0.9.1", "2"}},
		{"kind/shop/app=web pod web-1", []string{"web", "running", "-", "-", "unknown", "unknown"}},
		{"kind/shop/app=web pod web-2", []string{"web", "none", "-", "-", "-", "-"}},
	} {
		var got []string
		for l := range strings.Lines(out) {
			if strings.HasPrefix(l, c.target+" ") {
				got = strings.Fields(l)
				break
			}
		}
		if len(got) < 9 || strings.Join(got[3:9], " ") != strings.Join(c.want, " ") {
			t.Errorf("%s: %q, want %q in\n%s", c.target, got, c.want, out)
		}
	}
	if !strings.Contains(out, "an image from before -status") {
		t.Errorf("old image not noted:\n%s", out)
	}
}

// TestStatusPodGone: a pod entry whose pod is gone is a failed row and
// exit 1, and the other entries are still asked.
func TestStatusPodGone(t *testing.T) {
	f := newPodFixture(t, "")
	f.list(t, podObject("web-1", uid(2), true))
	if code := f.run("status"); code != exitError {
		t.Fatalf("exit %d, want 1: %s", code, f.stderr)
	}
	out := f.stdout.String()
	if !strings.Contains(out, "kind/shop/checkout-0") || !strings.Contains(out, "error") ||
		!strings.Contains(out, "kind/shop/app=web pod web-1") {
		t.Errorf("rows:\n%s", out)
	}
}

// TestStatusPodFails: `ready -status` failing other than with the old
// image's flag refusal (a corrupt state.json) is the row's error, never
// hidden behind a `ready -state` answer.
func TestStatusPodFails(t *testing.T) {
	f := newPodFixture(t, "")
	f.pod(t, podObject("checkout-0", uid(1), true))
	f.file(t, "statusfail-checkout-0", nil)
	if code := f.run("status", "kind/shop/checkout-0"); code != exitError {
		t.Fatalf("exit %d, want 1: %s", code, f.stderr)
	}
	if out := f.stdout.String(); !strings.Contains(out, "state: corrupt") || !strings.Contains(out, " error ") {
		t.Errorf("rows:\n%s", out)
	}
	for _, c := range f.calls(t) {
		if strings.Contains(strings.Join(c, " "), " -state") {
			t.Errorf("asked -state after a real failure: %q", c)
		}
	}
}

// TestList (task ph4): list prints the registry — every host and pod
// entry with each project, what it traces and its pass-through flags —
// and contacts nothing.
func TestList(t *testing.T) {
	f := newDeployFixture(t)
	if code := f.run("list"); code != exitOK || len(f.log) != 0 {
		t.Fatalf("exit %d, calls %q: %s", code, f.log, f.stderr)
	}
	var lines []string // the table's lines, single-spaced
	for l := range strings.Lines(f.stdout.String()) {
		lines = append(lines, strings.Join(strings.Fields(l), " "))
	}
	want := []string{
		"KIND TARGET PROJECT TRACES FLAGS NOTE",
		"host app1 shop pattern *orders* -duration 1h -",
		"host app1 billing listen-port 8443 - -",
		"host app2 shop pattern *orders* - -",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	p := newPodFixture(t, "")
	if code := p.run("list"); code != exitOK || p.calls(t) != nil {
		t.Fatalf("pods: exit %d: %s", code, p.stderr)
	}
	for _, want := range []string{"kind/shop/checkout-0", "kind/shop/app=web", "listen-port 8080",
		"debug mode, container app"} {
		if !strings.Contains(p.stdout.String(), want) {
			t.Errorf("missing %q in\n%s", want, p.stdout)
		}
	}
	if code := p.run("list", "extra"); code != exitUsage {
		t.Errorf("list extra: exit %d", code)
	}
}

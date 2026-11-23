package ctl

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"iormesh/internal/federate"
)

// statusJSON is one host's `status -json` answer holding reports.
func statusJSON(reports ...string) string {
	return "[" + strings.Join(reports, ",") + "]"
}

// ended is a report of project's session that ended for reason, with
// operator_stop as given ("" leaves it out: a tracer from before it).
func ended(project, reason, operatorStop string, ready int) string {
	op := ""
	if operatorStop != "" {
		op = `,"operator_stop":` + operatorStop
	}
	return `{"project":"` + project + `","unit_state":"not loaded","state":"ended","ready_segments":` +
		strconv.Itoa(ready) + `,"session":{"end_reason":"` + reason + `"` + op + `}}`
}

const runningShop = `{"project":"shop","unit_state":"active/running","state":"running","ready_segments":1,` +
	`"session":{"operator_stop":false}}`

// collectLog is collect's calls for host's projects, in order.
func collectLog(f *deployFixture, host string, projects ...string) [][]string {
	calls := [][]string{call(host, "run", "true")}
	for _, p := range projects {
		remote := "/opt/iormesh/traces/" + p + "/ready"
		calls = append(calls, call(host, "run", "test", "-d", remote),
			call(host, "fetch", remote, f.store+"/"+p+"/"+host))
	}
	return calls
}

// TestEnsureRestartsEnds (task zh4): per host, ensure reads the tracer's
// `status -json`, starts the sessions that ended on their own (a
// duration bound here) with deploy's start argv, leaves the one the
// operator stopped and the one still running, then collects every
// project of the host. Nothing is upgraded or pushed.
func TestEnsureRestartsEnds(t *testing.T) {
	f := newDeployFixture(t)
	f.answers["app1 "+strings.Join(sudoTr, " ")+" status -json"] = reply{out: statusJSON(
		ended("shop", "duration", "false", 0), ended("billing", "stop", "true", 0))}
	f.answers["app2 "+strings.Join(sudoTr, " ")+" status -json"] = reply{out: statusJSON(runningShop)}
	if code := f.run("ensure"); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr)
	}
	checkLog(t, f.log,
		[][]string{sudoCall("app1", "status", "-json"),
			sudoCall("app1", "start", "-project", "shop", "-pattern=*orders*", "-duration", "1h")},
		collectLog(f, "app1", "shop", "billing"),
		[][]string{sudoCall("app2", "status", "-json")},
		collectLog(f, "app2", "shop"))
	out := f.stdout.String()
	for _, want := range []string{"app1: project shop: ended (duration), restarted",
		"app1: project billing: stopped by the operator, left stopped", "app2: project shop: running"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
}

// TestEnsureDecisions: what ensure does with each state. Ends the
// operator did not ask for — targets gone, a tracer error, a reboot's
// SIGTERM (stop, operator_stop false), a crash (dead) — are restarted;
// an operator stop, a stop from a tracer that cannot tell (no
// operator_stop) and a live unit are left; a project the host never ran
// is deploy's to start, not ensure's.
func TestEnsureDecisions(t *testing.T) {
	for _, c := range []struct {
		name, report string
		restart      bool
		note         string
	}{
		{"targets exited", ended("shop", "targets_exited", "false", 0), true, "restarted"},
		{"tracer error", ended("shop", "error", "false", 0), true, "restarted"},
		{"reboot", ended("shop", "stop", "false", 0), true, "restarted"},
		{"crash", `{"project":"shop","unit_state":"failed/failed","state":"dead","ready_segments":0,` +
			`"session":{"operator_stop":false}}`, true, "restarted"},
		{"dead after an operator stop", `{"project":"shop","unit_state":"not loaded","state":"dead",` +
			`"ready_segments":0,"session":{"operator_stop":true}}`, false, "stopped by the operator"},
		{"old tracer stop", ended("shop", "stop", "", 0), false, "cannot tell"},
		{"live unit", runningShop, false, "running"},
		{"pending attach", `{"project":"shop","unit_state":"activating/start","ready_segments":0}`,
			false, "running"},
		{"never started", ``, false, "deploy"},
	} {
		f := newDeployFixture(t)
		f.answers["app2 "+strings.Join(sudoTr, " ")+" status -json"] = reply{out: statusJSON(c.report)}
		if code := f.run("ensure", "app2"); code != exitOK {
			t.Errorf("%s: exit %d: %s", c.name, code, f.stderr)
			continue
		}
		started := false
		for _, l := range f.log {
			if len(l) > 5 && l[5] == "start" {
				started = true
			}
		}
		if started != c.restart || !strings.Contains(f.stdout.String(), c.note) {
			t.Errorf("%s: started %v, want %v; stdout lacks %q:\n%s", c.name, started, c.restart, c.note,
				f.stdout)
		}
	}
}

// TestEnsureDiskLimit: a disk_limit end is collected first and restarted
// only once its ready/ is empty — restarting it into a full disk would
// end it again at once, tick after tick. A collect that leaves segments
// behind, or fails, starts nothing.
func TestEnsureDiskLimit(t *testing.T) {
	key := "app2 " + strings.Join(sudoTr, " ") + " status -json"
	for _, c := range []struct {
		name     string
		after    int
		fetchErr error
		restart  bool
		code     int
		note     string
	}{
		{"collected", 0, nil, true, exitOK, "restarted after collecting"},
		{"still full", 2, nil, false, exitOK, "2 segments still in ready/"},
		{"collect failed", 0, errors.New("rsync: broken pipe"), false, exitError, "not restarted"},
	} {
		f := newDeployFixture(t)
		f.answers[key] = reply{out: statusJSON(ended("shop", "disk_limit", "false", 5))}
		after := statusJSON(ended("shop", "disk_limit", "false", c.after))
		f.e.transport = func(host string) (federate.Transport, error) {
			ft := f.transport(host)
			ft.fetch = func(string, string, string) error {
				f.answers[key] = reply{out: after}
				return c.fetchErr
			}
			return ft, nil
		}
		code := f.run("ensure", "app2")
		started := false
		for _, l := range f.log {
			if len(l) > 5 && l[5] == "start" {
				started = true
			}
		}
		out := f.stdout.String() + f.stderr.String()
		if code != c.code || started != c.restart || !strings.Contains(out, c.note) {
			t.Errorf("%s: exit %d, started %v; want %d, %v and %q:\n%s", c.name, code, started, c.code,
				c.restart, c.note, out)
		}
		if c.restart {
			checkLog(t, f.log, [][]string{sudoCall("app2", "status", "-json")}, collectLog(f, "app2", "shop"),
				[][]string{sudoCall("app2", "status", "-json"),
					sudoCall("app2", "start", "-project", "shop", "-pattern=*orders*")})
		}
	}
}

// TestEnsureFailures: a host that cannot be reached, whose status cannot
// be read, or whose start fails is reported and the next host still
// ensured; the command then exits 1 naming it. A report carrying an
// error is that project's failure, not the host's: its other projects
// are still handled and the host collected.
func TestEnsureFailures(t *testing.T) {
	f := newDeployFixture(t)
	f.answers["app1 "] = reply{err: errors.New("ssh app1: connection refused")}
	f.answers["app2 "+strings.Join(sudoTr, " ")+" status -json"] = reply{out: statusJSON(runningShop)}
	if code := f.run("ensure"); code != exitError {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(f.stderr.String(), "ensure failed on 1 of 2 hosts: app1") {
		t.Errorf("stderr %q", f.stderr)
	}
	checkLog(t, f.log, [][]string{sudoCall("app1", "status", "-json"), sudoCall("app2", "status", "-json")},
		collectLog(f, "app2", "shop"))

	f = newDeployFixture(t)
	f.answers["app2 "+strings.Join(sudoTr, " ")+" status -json"] = reply{
		out: statusJSON(ended("shop", "duration", "false", 0))}
	// The remote start's diagnosis, its unit's journal tail (task zk4),
	// reaches the operator: the reason the run never attached.
	f.answers["app2 "+strings.Join(sudoTr, " ")+" start"] = reply{err: errors.New(startDiagnosis)}
	if code := f.run("ensure", "app2"); code != exitError ||
		!strings.Contains(f.stderr.String(), "project shop: start: exit status 1") ||
		!strings.Contains(f.stderr.String(), "nothing listens on TCP port 9") {
		t.Errorf("failed start: exit %d, stderr %q", code, f.stderr)
	}
	// The host is still collected after a failed start.
	if l := f.log[len(f.log)-1]; l[1] != "fetch" {
		t.Errorf("not collected after a failed start: %q", f.log)
	}

	f = newDeployFixture(t)
	f.answers["app1 "+strings.Join(sudoTr, " ")+" status -json"] = reply{out: statusJSON(
		`{"project":"shop","unit_state":"x","error":"state: corrupt"}`, ended("billing", "duration", "false", 0)),
		err: exitStatus(t, 1)}
	if code := f.run("ensure", "app1"); code != exitError || !strings.Contains(f.stderr.String(), "state: corrupt") {
		t.Errorf("report error: exit %d, stderr %q", code, f.stderr)
	}
	checkLog(t, f.log, [][]string{sudoCall("app1", "status", "-json"),
		sudoCall("app1", "start", "-project", "billing", "-listen-port=8443")},
		collectLog(f, "app1", "shop", "billing"))
}

// TestEnsureRefuses: a pod entry (ensure keeps hosts' sessions; a pod's
// tracer is its debug container's), an unregistered host and a stray
// project fail before any host is contacted.
func TestEnsureRefuses(t *testing.T) {
	f := newDeployFixture(t)
	for _, args := range [][]string{{"ensure", "kind/shop/app=web"}, {"ensure", "nope"},
		{"ensure", "-project", "nope"}} {
		if code := f.run(args...); code == exitOK || len(f.log) != 0 {
			t.Errorf("%v: exit %d, calls %q", args, code, f.log)
		}
	}
}

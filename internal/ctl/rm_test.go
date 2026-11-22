package ctl

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/federate"
)

// rmPrelude is rm's calls before the first project: reachability and the
// installed-tracer check.
func rmPrelude(host string) [][]string {
	return [][]string{probeCall(host), call(host, "run", "test", "-x", "/opt/iormesh/bin/iormesh")}
}

// The sweep's calls before an uninstall: the trace dir's project dirs,
// then the files still left in it.
var (
	listDirs = []string{"find", "/opt/iormesh/traces", "-mindepth", "1", "-maxdepth", "1", "-type", "d",
		"-print"}
	listLeft = []string{"find", "/opt/iormesh/traces", "-mindepth", "2", "-type", "f",
		"(", "-path", "*/ready/*", "-o", "-name", ".active-*", ")", "-print"}
)

// sweepLog is the sweep of a trace dir with nothing beyond the registry.
func sweepLog(host string) [][]string {
	return [][]string{call(host, "run", "test", "-d", "/opt/iormesh/traces"),
		call(host, "run", listDirs...), call(host, "run", listLeft...)}
}

// uninstallLog is the sweep and the uninstall.
func uninstallLog(host string) [][]string {
	return append(sweepLog(host), sudoCall(host, "uninstall"))
}

// hosts is the registry's host/project list.
func (f *deployFixture) hosts(t *testing.T) []string {
	t.Helper()
	r, err := federate.LoadRegistry(f.registry)
	if err != nil {
		t.Fatal(err)
	}
	var hs []string
	for _, s := range r.Servers {
		for _, p := range s.Projects {
			hs = append(hs, s.Host+"/"+p.Name)
		}
	}
	return hs
}

// TestRmTeardown: rm HOST stops the live session (its last segment is
// published), syncs every project off, sweeps, uninstalls, and only then
// unregisters; rm -project stops and syncs that project and uninstalls
// only when it was the host's last.
func TestRmTeardown(t *testing.T) {
	f := newDeployFixture(t)
	f.answers["app1 systemctl show --property=ActiveState --value -- iormesh-shop.service"] = reply{out: "active\n"}
	f.mustRun(t, "rm", "app1")
	checkLog(t, f.log, rmPrelude("app1"),
		[][]string{showCall("app1", "shop"), sudoCall("app1", "stop", "-project", "shop")},
		f.collectLog("app1", "shop"),
		[][]string{showCall("app1", "billing")}, f.collectLog("app1", "billing"),
		uninstallLog("app1"))
	if got := f.hosts(t); !reflect.DeepEqual(got, []string{"app2/shop"}) {
		t.Errorf("registry after rm: %v", got)
	}

	f = newDeployFixture(t)
	f.mustRun(t, "rm", "-project", "shop", "app1")
	checkLog(t, f.log, rmPrelude("app1"), [][]string{showCall("app1", "shop")}, f.collectLog("app1", "shop"))
	f.mustRun(t, "rm", "-project", "shop", "app2")
	checkLog(t, f.log, rmPrelude("app2"), [][]string{showCall("app2", "shop")}, f.collectLog("app2", "shop"),
		uninstallLog("app2"))
	if got := f.hosts(t); !reflect.DeepEqual(got, []string{"app1/billing"}) {
		t.Errorf("registry after rm: %v", got)
	}
}

// TestRmSweepsUnlisted: before uninstalling, a project directory the
// registry no longer lists but deploy's record still does (dropped by an
// earlier rm -project whose record edit did not land, say) is stopped and
// collected too; a name that is no label is not.
func TestRmSweepsUnlisted(t *testing.T) {
	f := newDeployFixture(t)
	rec := deployedRecord{}
	rec.set("app2", "manual", []string{"start", "-project", "manual"}, "v1")
	if err := rec.save(f.record); err != nil {
		t.Fatal(err)
	}
	f.answers["app2 find /opt/iormesh/traces -mindepth 1"] = reply{
		out: "/opt/iormesh/traces/shop\n/opt/iormesh/traces/manual\n/opt/iormesh/traces/.odd\n"}
	f.answers["app2 systemctl show --property=ActiveState --value -- iormesh-manual.service"] = reply{out: "active\n"}
	f.mustRun(t, "rm", "app2")
	checkLog(t, f.log, rmPrelude("app2"), [][]string{showCall("app2", "shop")}, f.collectLog("app2", "shop"),
		[][]string{call("app2", "run", "test", "-d", "/opt/iormesh/traces"), call("app2", "run", listDirs...),
			showCall("app2", "manual"), sudoCall("app2", "stop", "-project", "manual")},
		f.collectLog("app2", "manual"),
		[][]string{call("app2", "run", listLeft...), sudoCall("app2", "uninstall")})
}

// TestRmSweepSkipsForeign: a project directory the server reports that
// neither the registry nor deploy's record ever listed for the host is not
// stopped or collected — a compromised server must not plant segments in
// another project's store — so its files are leftovers: rm refuses
// without -force, with the registry unchanged, and -force uninstalls.
func TestRmSweepSkipsForeign(t *testing.T) {
	f := newDeployFixture(t)
	f.answers["app2 find /opt/iormesh/traces -mindepth 1"] = reply{
		out: "/opt/iormesh/traces/shop\n/opt/iormesh/traces/payments\n"}
	f.answers["app2 find /opt/iormesh/traces -mindepth 2"] = reply{
		out: "/opt/iormesh/traces/payments/ready/x.parquet\n"}
	if code := f.run("rm", "app2"); code != exitError ||
		!strings.Contains(f.stderr.String(), "1 trace files could not be synced off: "+
			"/opt/iormesh/traces/payments/ready/x.parquet; nothing uninstalled") {
		t.Fatalf("exit %d, stderr %s", code, f.stderr)
	}
	sweepStart := [][]string{call("app2", "run", "test", "-d", "/opt/iormesh/traces"),
		call("app2", "run", listDirs...)}
	checkLog(t, f.log, rmPrelude("app2"), [][]string{showCall("app2", "shop")}, f.collectLog("app2", "shop"),
		sweepStart, [][]string{call("app2", "run", listLeft...)})
	if got := f.hosts(t); len(got) != 3 {
		t.Errorf("registry changed: %v", got)
	}
	f.mustRun(t, "rm", "-force", "app2")
	checkLog(t, f.log, rmPrelude("app2"), [][]string{showCall("app2", "shop")}, f.collectLog("app2", "shop"),
		sweepStart, [][]string{call("app2", "run", listLeft...), sudoCall("app2", "uninstall")})
	if _, err := os.Stat(filepath.Join(f.store, "payments")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("store/payments: %v, want it absent", err)
	}
}

// TestRmLeftovers: files still in the trace dir after the sweep (an
// unclean end's active file, a segment under an odd name) refuse the
// uninstall, naming them, with the registry unchanged; -force uninstalls
// anyway with a warning.
func TestRmLeftovers(t *testing.T) {
	f := newDeployFixture(t)
	left := "/opt/iormesh/traces/shop/.active-0a1b2c3d-events.parquet\n/opt/iormesh/traces/.odd/ready/x.parquet\n"
	f.answers["app2 find /opt/iormesh/traces -mindepth 2"] = reply{out: left}
	if code := f.run("rm", "app2"); code != exitError ||
		!strings.Contains(f.stderr.String(), "2 trace files could not be synced off: "+
			"/opt/iormesh/traces/shop/.active-0a1b2c3d-events.parquet /opt/iormesh/traces/.odd/ready/x.parquet; "+
			"nothing uninstalled") {
		t.Fatalf("exit %d, stderr %s", code, f.stderr)
	}
	assertNoUninstall(t, f.log)
	if got := f.hosts(t); len(got) != 3 {
		t.Errorf("registry changed: %v", got)
	}
	f.mustRun(t, "rm", "-force", "app2")
	checkLog(t, f.log, rmPrelude("app2"), [][]string{showCall("app2", "shop")}, f.collectLog("app2", "shop"),
		uninstallLog("app2"))
	if !strings.Contains(f.stderr.String(), "-force: uninstalling, they are lost") {
		t.Errorf("stderr: %s", f.stderr)
	}
}

// TestRmFailureKeepsRegistry: a failed or unclean stop, a failed sync, a
// check that fails other than "no" (ssh 255) or a failed uninstall stops
// rm before the registry changes, and nothing after the failure runs —
// in particular no uninstall over traces that did not sync.
func TestRmFailureKeepsRegistry(t *testing.T) {
	all := []string{"app1/shop", "app1/billing", "app2/shop"}
	cases := map[string]func(f *deployFixture){
		"sync": func(f *deployFixture) {
			f.fetchErrs["app1 "+readyOf("billing")] = errors.New("rsync: write failed")
		},
		"unclean stop": func(f *deployFixture) {
			f.answers["app1 systemctl show"] = reply{out: "active\n"}
			f.answers["app1 sudo -n /opt/iormesh/bin/iormesh stop"] = reply{
				err: errors.New("stopped iormesh-shop.service, but state.json says \"running\"")}
		},
		"test -d 255": func(f *deployFixture) {
			f.answers["app1 test -d "+readyOf("billing")] = reply{err: exitStatus(t, 255)}
		},
		"test -x 255": func(f *deployFixture) {
			f.answers["app1 test -x"] = reply{err: exitStatus(t, 255)}
		},
		"uninstall": func(f *deployFixture) {
			f.answers["app1 sudo -n /opt/iormesh/bin/iormesh uninstall"] = reply{err: errors.New("exit status 1")}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newDeployFixture(t)
			setup(f)
			if code := f.run("rm", "app1"); code != exitError ||
				!strings.Contains(f.stderr.String(), "registry unchanged") {
				t.Fatalf("exit %d, stderr %s", code, f.stderr)
			}
			if got := f.hosts(t); !reflect.DeepEqual(got, all) {
				t.Errorf("registry changed: %v", got)
			}
			if name != "uninstall" {
				assertNoUninstall(t, f.log)
			}
		})
	}
}

// TestRmForceUncleanStop: -force goes on past a stop whose run did not
// end cleanly, warning, and uninstalls.
func TestRmForceUncleanStop(t *testing.T) {
	f := newDeployFixture(t)
	f.answers["app2 systemctl show"] = reply{out: "active\n"}
	f.answers["app2 sudo -n /opt/iormesh/bin/iormesh stop"] = reply{err: errors.New("not ended")}
	f.mustRun(t, "rm", "-force", "app2")
	if !strings.Contains(f.stderr.String(), "app2: project shop: stop: not ended; -force: going on") ||
		!slices.ContainsFunc(f.log, func(c []string) bool { return slices.Contains(c, "uninstall") }) {
		t.Errorf("stderr %s, calls %q", f.stderr, f.log)
	}
}

// TestRmUnreachable: an unreachable host is warned about and only dropped
// from the registry; a host without the tracer (test -x exit 1) has
// nothing to tear down.
func TestRmUnreachable(t *testing.T) {
	f := newDeployFixture(t)
	f.answers["app1 true"] = reply{err: errors.New("ssh: Could not resolve hostname app1")}
	f.mustRun(t, "rm", "app1")
	checkLog(t, f.log, [][]string{probeCall("app1")})
	if !strings.Contains(f.stderr.String(), "warning: app1: host unreachable") {
		t.Errorf("stderr: %s", f.stderr)
	}
	f.answers["app2 test -x"] = reply{err: exitStatus(t, 1)}
	f.mustRun(t, "rm", "app2")
	checkLog(t, f.log, rmPrelude("app2"))
	if got := f.hosts(t); len(got) != 0 {
		t.Errorf("registry after rm: %v", got)
	}
}

func assertNoUninstall(t *testing.T, log [][]string) {
	t.Helper()
	if slices.ContainsFunc(log, func(c []string) bool { return slices.Contains(c, "uninstall") }) {
		t.Errorf("uninstalled: %q", log)
	}
}

package ctl

import (
	"errors"
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

// hosts is the registry's host list.
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
// published), syncs every project off, uninstalls, and only then
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
		[][]string{sudoCall("app1", "uninstall")})
	if got := f.hosts(t); !reflect.DeepEqual(got, []string{"app2/shop"}) {
		t.Errorf("registry after rm: %v", got)
	}

	f = newDeployFixture(t)
	f.mustRun(t, "rm", "-project", "shop", "app1")
	checkLog(t, f.log, rmPrelude("app1"), [][]string{showCall("app1", "shop")}, f.collectLog("app1", "shop"))
	f.mustRun(t, "rm", "-project", "shop", "app2")
	checkLog(t, f.log, rmPrelude("app2"), [][]string{showCall("app2", "shop")}, f.collectLog("app2", "shop"),
		[][]string{sudoCall("app2", "uninstall")})
	if got := f.hosts(t); !reflect.DeepEqual(got, []string{"app1/billing"}) {
		t.Errorf("registry after rm: %v", got)
	}
}

// TestRmFailureKeepsRegistry: a failed stop, sync or uninstall stops rm
// before the registry changes, and nothing after the failure runs — in
// particular no uninstall over traces that did not sync.
func TestRmFailureKeepsRegistry(t *testing.T) {
	all := []string{"app1/shop", "app1/billing", "app2/shop"}
	cases := map[string]func(f *deployFixture){
		"sync": func(f *deployFixture) {
			f.fetchErrs["app1 "+readyOf("billing")] = errors.New("rsync: write failed")
		},
		"stop": func(f *deployFixture) {
			f.answers["app1 systemctl show"] = reply{out: "active\n"}
			f.answers["app1 sudo -n /opt/iormesh/bin/iormesh stop"] = reply{err: errors.New("exit status 1")}
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
			if name != "uninstall" && slices.ContainsFunc(f.log, func(c []string) bool {
				return slices.Contains(c, "uninstall")
			}) {
				t.Errorf("uninstalled after a failed %s: %q", name, f.log)
			}
		})
	}
}

// TestRmUnreachable: an unreachable host is warned about and only dropped
// from the registry; a host without the tracer has nothing to tear down.
func TestRmUnreachable(t *testing.T) {
	f := newDeployFixture(t)
	f.answers["app1 true"] = reply{err: errors.New("ssh: Could not resolve hostname app1")}
	f.mustRun(t, "rm", "app1")
	checkLog(t, f.log, [][]string{probeCall("app1")})
	if !strings.Contains(f.stderr.String(), "warning: app1: host unreachable") {
		t.Errorf("stderr: %s", f.stderr)
	}
	f.answers["app2 test -x"] = reply{err: errors.New("exit status 1")}
	f.mustRun(t, "rm", "app2")
	checkLog(t, f.log, rmPrelude("app2"))
	if got := f.hosts(t); len(got) != 0 {
		t.Errorf("registry after rm: %v", got)
	}
}

package ctl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/federate"
)

// applyFixture is the shop mesh's store (buildFixture) plus what deploy
// needs: a pinned key, signed builds, a deploy record and a fake
// transport whose every call is logged. The shop's app1 dialed the
// untraced database 203.0.113.9:5432.
type applyFixture struct {
	*buildFixture
	dist    string
	log     [][]string
	answers map[string]reply
}

func newApplyFixture(t *testing.T, x *federate.Expand) *applyFixture {
	t.Helper()
	f := &applyFixture{buildFixture: newBuildFixture(t), dist: t.TempDir(), answers: map[string]reply{}}
	f.saveRegistry(t, x)
	pub, priv := genKey(t)
	keyPath := filepath.Join(t.TempDir(), keyFileName)
	text, _ := pub.MarshalText()
	writeFile(t, keyPath, text, 0o600)
	signed(t, priv, filepath.Join(f.dist, "iormesh-linux-amd64"))
	record := filepath.Join(t.TempDir(), deployedFileName)
	f.e.releaseKey = func() (string, error) { return keyPath, nil }
	f.e.deployed = func() (string, error) { return record, nil }
	d := &deployFixture{answers: f.answers}
	f.e.transport = func(host string) (federate.Transport, error) {
		return fakeTransport{host: host, log: &f.log, respond: func(host string, argv []string) ([]byte, error) {
			if argv[0] == "cat" {
				if r, ok := f.answers[host+" cat"]; ok {
					return []byte(r.out), r.err
				}
				return []byte("id-" + host + "\n"), nil
			}
			return d.respond(host, argv)
		}}, nil
	}
	return f
}

// apply runs expand -apply with the fixture's builds.
func (f *applyFixture) apply(args ...string) int {
	f.log = nil
	return f.run(slices.Concat([]string{"expand", "-apply", "-dist", f.dist}, args)...)
}

// registry reads the fixture's registry back.
func (f *applyFixture) registry(t *testing.T) *federate.Registry {
	t.Helper()
	path, _ := f.e.registry()
	r, err := federate.LoadRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	return &r
}

// contacted is the hosts the fake transport was used for, in order.
func (f *applyFixture) contacted() []string {
	var hosts []string
	for _, c := range f.log {
		if !slices.Contains(hosts, c[0]) {
			hosts = append(hosts, c[0])
		}
	}
	return hosts
}

var dbAllowed = &federate.Expand{Allow: federate.Allow{CIDRs: []string{"203.0.113.0/24"}}}

// TestExpandApply: the allowed database is registered at hop 1 under the
// project that dialed it, bound to its listening port, and receives
// exactly deploy's steps after the unprivileged machine-id read — and no
// other host is contacted. A second round enrolls nothing: the address is
// registered now (the visited set), so expansion terminates.
func TestExpandApply(t *testing.T) {
	f := newApplyFixture(t, dbAllowed)
	if code := f.apply(); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr, f.stdout)
	}
	srv, err := f.registry(t).Lookup("203.0.113.9")
	if err != nil {
		t.Fatal(err)
	}
	want := federate.Server{Host: "203.0.113.9", Hop: 1, Projects: []federate.Project{{Name: "shop",
		Target: federate.Target{ListenPort: 5432}}}}
	if !serversEqual(srv, want) {
		t.Errorf("registered %+v, want %+v", srv, want)
	}
	const db = "203.0.113.9"
	checkLog(t, f.log, [][]string{call(db, "run", "cat", "/etc/machine-id")}, preflightLog(db, "amd64"),
		startLog(db, "shop", "start", "-project", "shop", "-listen-port=5432"))
	if !strings.Contains(f.stdout.String(), "enrolled 203.0.113.9 hop 1 project shop listen-port 5432") {
		t.Errorf("stdout:\n%s", f.stdout)
	}
	if code := f.apply(); code != exitOK || len(f.log) != 0 ||
		!strings.Contains(f.stdout.String(), "nothing to enroll") {
		t.Errorf("second round: exit %d, contacted %v\n%s", code, f.contacted(), f.stdout)
	}
}

func serversEqual(a, b federate.Server) bool {
	return a.Host == b.Host && a.Hop == b.Hop && slices.EqualFunc(a.Projects, b.Projects,
		func(x, y federate.Project) bool {
			return x.Name == y.Name && x.Target == y.Target && slices.Equal(x.Flags, y.Flags)
		})
}

// TestExpandApplyOutsideAllowlist: a peer the allowlist does not cover is
// listed but never contacted, and the registry is left as it was; -hops 0
// likewise lists the allowed peer and touches nothing.
func TestExpandApplyOutsideAllowlist(t *testing.T) {
	for name, tc := range map[string]struct {
		x    *federate.Expand
		args []string
	}{
		"no expand section": {nil, nil},
		"another range":     {&federate.Expand{Allow: federate.Allow{CIDRs: []string{"10.0.0.0/8"}}}, nil},
		"hops zero":         {dbAllowed, []string{"-hops", "0"}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newApplyFixture(t, tc.x)
			before := f.registry(t)
			if code := f.apply(tc.args...); code != exitOK {
				t.Fatalf("exit %d: %s", code, f.stderr)
			}
			if len(f.log) != 0 {
				t.Errorf("contacted %v", f.contacted())
			}
			if !strings.Contains(f.stdout.String(), "203.0.113.9:5432") {
				t.Errorf("the peer is not listed:\n%s", f.stdout)
			}
			if after := f.registry(t); len(after.Servers) != len(before.Servers) {
				t.Errorf("registry grew: %+v", after.Servers)
			}
		})
	}
}

// TestExpandApplyKnownMachine: a candidate whose machine-id is a traced
// host's (hostA, app1's) is the same machine at another address: it is
// unregistered again, nothing privileged runs, and expand exits 1.
func TestExpandApplyKnownMachine(t *testing.T) {
	f := newApplyFixture(t, dbAllowed)
	f.answers["203.0.113.9 cat"] = reply{out: "hostA\n"}
	if code := f.apply(); code != exitError || !strings.Contains(f.stderr.String(), "same machine as app1") {
		t.Fatalf("exit %d: %s", code, f.stderr)
	}
	for _, c := range f.log {
		if slices.Contains(c, "sudo") {
			t.Errorf("privileged call %q", c)
		}
	}
	if _, err := f.registry(t).Lookup("203.0.113.9"); !errors.Is(err, federate.ErrUnknownHost) {
		t.Errorf("still registered: %v", err)
	}
}

// TestExpandApplyRefusals: a damaged segment (the visited set is
// incomplete), a missing release key and -json each stop -apply before
// any host is contacted or the registry changes.
func TestExpandApplyRefusals(t *testing.T) {
	f := newApplyFixture(t, dbAllowed)
	bad := f.app2Files[len(f.app2Files)-1]
	data, err := os.ReadFile(bad)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, bad, data[:len(data)/2], 0o600)
	if code := f.apply(); code != exitError || len(f.log) != 0 ||
		!strings.Contains(f.stderr.String(), "nothing enrolled") {
		t.Errorf("bad segment: exit %d, contacted %v: %s", code, f.contacted(), f.stderr)
	}
	writeFile(t, bad, data, 0o600)
	f.e.releaseKey = func() (string, error) { return filepath.Join(t.TempDir(), "none.pub"), nil }
	if code := f.apply(); code != exitError || len(f.log) != 0 {
		t.Errorf("no key: exit %d, contacted %v", code, f.contacted())
	}
	if _, err := f.registry(t).Lookup("203.0.113.9"); err == nil {
		t.Error("registered although -apply was refused")
	}
	if code := f.apply("-json"); code != exitUsage {
		t.Errorf("-apply -json: exit %d, want %d", code, exitUsage)
	}
}

// TestExpandApplyFailedDeploy: a host whose deploy fails stays
// registered, and expand says how to retry or remove it; an unknown ssh
// host key adds the known_hosts hint (no trust on first use).
func TestExpandApplyFailedDeploy(t *testing.T) {
	f := newApplyFixture(t, dbAllowed)
	f.answers["203.0.113.9 test -x"] = reply{err: errors.New("exit status 1")}
	if code := f.apply(); code != exitError ||
		!strings.Contains(f.stderr.String(), "203.0.113.9 stay registered: iormeshctl deploy HOST retries") {
		t.Fatalf("exit %d: %s", code, f.stderr)
	}
	if _, err := f.registry(t).Lookup("203.0.113.9"); err != nil {
		t.Errorf("unregistered after a failed deploy: %v", err)
	}

	g := newApplyFixture(t, dbAllowed)
	g.answers["203.0.113.9 cat"] = reply{err: errors.New("ssh: Host key verification failed.")}
	if code := g.apply(); code != exitError || !strings.Contains(g.stderr.String(), "ssh-keyscan 203.0.113.9") {
		t.Errorf("host key: exit %d: %s", code, g.stderr)
	}
}

// TestApplierOneMachineTwoAddresses: a new machine reached at two
// addresses in one round is deployed to once; the second address is a
// duplicate, unregistered again, and not named as a failed host.
func TestApplierOneMachineTwoAddresses(t *testing.T) {
	f := newApplyFixture(t, dbAllowed)
	f.answers["203.0.113.9 cat"] = reply{out: "id-same\n"}
	f.answers["203.0.113.10 cat"] = reply{out: "id-same\n"}
	d, rec, err := newDeployer(f.e, f.dist, "", false)
	if err != nil {
		t.Fatal(err)
	}
	a := &applier{d: d, known: map[string]string{}}
	shop := []federate.Project{{Name: "shop", Target: federate.Target{ListenPort: 5432}}}
	servers := []federate.Server{{Host: "203.0.113.9", Hop: 1, Projects: shop},
		{Host: "203.0.113.10", Hop: 1, Projects: shop}}
	if err := d.all(context.Background(), servers, rec, a.host); err == nil ||
		!strings.Contains(err.Error(), "203.0.113.10") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(f.stderr.String(), "same machine as 203.0.113.9") {
		t.Errorf("stderr: %s", f.stderr)
	}
	for _, c := range f.log {
		if c[0] == "203.0.113.10" && slices.Contains(c, "sudo") {
			t.Errorf("deployed to the second address: %q", c)
		}
	}
	if !slices.Equal(a.dups, []string{"203.0.113.10"}) || len(a.failed) != 0 {
		t.Errorf("dups %v, failed %v", a.dups, a.failed)
	}
}

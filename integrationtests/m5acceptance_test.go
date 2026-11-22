package integrationtests

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/segment"
	"iormesh/internal/viewmodel"
)

// M5's acceptance on the rig (task rb4; PLAN.md M5 "Acceptance"): the
// microservices spread of task 1f4 — four services on three emulated
// hosts, each host its own project and tracer, under the Java agent — is
// collected into a store as `iormeshctl collect` lays one out, its
// servers named in the service database, and built by the real
// iormeshctl binary. Then every surface is walked from the mesh down to
// one step's Java stack: `show` (text), the TUI (its model, as task ob4's
// ctl test drives it) and the local site (its embedded model; in a
// headless browser with IORMESH_SITE_BROWSER=1). The SVG exports are
// rebuilt byte for byte and held to goldens, the checkout's sequence to
// the declared hops and file work, and the eval harness is run end to
// end over the same store with a scripted stand-in model — never a real
// one: that needs the owner's model command (PLAN.md M5 "LLM
// introspection eval").
//
// What the rig cannot do is M4's SSH deploy and collect: the store is
// filled from the tracers' ready segments directly, as task tb4's
// expansion test starts the tracers itself. Sequencing relaxation
// (PLAN.md): this is "passes on the rig", not M5's acceptance, until the
// real-host re-check (task 3f4).

// ctlBinaryDefault is iormeshctl, built by mage build.
const ctlBinaryDefault = "../iormeshctl"

// spreadNaming are the naming rules the operator adds: each server
// endpoint of the spread named after its service.
var spreadNaming = [][3]string{
	{rigServerAddr, spreadTLSPort, "orders"},
	{rigClientAddr, spreadPlainPort, "inventory"},
	{rigContainerAddr, spreadTLSPort, "billing"},
}

// viewsBuild is one build of the spread's store by iormeshctl: its
// paths, the model it wrote, and what the test knows of the run — each
// workload's role by PID, the harness's temporary directory.
type viewsBuild struct {
	ctl, store, db, builds, home string
	// dir is the build's output directory.
	dir   string
	model viewmodel.Model
	roles map[uint32]string
	tmp   string
}

// newViewsBuild exports the spread's segments into a store, names the
// servers and builds.
func newViewsBuild(t *testing.T, h *Harness, s spreadRun) *viewsBuild {
	t.Helper()
	root := filepath.Join(h.Dir, "controller")
	b := &viewsBuild{ctl: absPath(t, ctlBinaryDefault), store: filepath.Join(root, "store"),
		db: filepath.Join(root, "services.db"), builds: filepath.Join(root, "builds"),
		home: filepath.Join(root, "home"), roles: spreadRoles(s), tmp: h.Tmp}
	for _, d := range []string{root, b.home} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	exportSpreadStore(t, s, b.store)
	for _, n := range spreadNaming {
		b.run(t, "services", "name", "-db", b.db, "-port", n[1], n[0], n[2])
	}
	t.Logf("iormeshctl build:\n%s", b.build(t))
	return b
}

// build runs `iormeshctl build` and reads the model it wrote.
func (b *viewsBuild) build(t *testing.T) string {
	t.Helper()
	out := b.run(t, "build", "-store", b.store, "-db", b.db, "-builds", b.builds)
	b.dir = filepath.Join(b.builds, strings.Join(spreadProjects(), "+"))
	data, err := os.ReadFile(filepath.Join(b.dir, "model.json"))
	if err != nil {
		t.Fatal(err)
	}
	b.model = viewmodel.Model{}
	if err := json.Unmarshal(data, &b.model); err != nil {
		t.Fatalf("model.json: %v", err)
	}
	return out
}

// run runs iormeshctl and returns its stdout and stderr; a failure
// fails the test. HOME and the XDG directories point into the
// controller's scratch directory, so no default path reaches root's own.
func (b *viewsBuild) run(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command(b.ctl, args...)
	cmd.Env = append(os.Environ(), "HOME="+b.home, "XDG_CONFIG_HOME="+filepath.Join(b.home, "config"),
		"XDG_DATA_HOME="+filepath.Join(b.home, "data"), "XDG_CACHE_HOME="+filepath.Join(b.home, "cache"),
		"XDG_STATE_HOME="+filepath.Join(b.home, "state"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("iormeshctl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// spreadProjects are the spread's projects, sorted as build names them.
func spreadProjects() []string {
	var p []string
	for _, n := range spreadHostNames {
		p = append(p, n[1])
	}
	slices.Sort(p)
	return p
}

// spreadRoles maps each workload's PID to its role: spreadServices
// appended each host's workloads in service order.
func spreadRoles(s spreadRun) map[uint32]string {
	next := make([]int, len(s.hosts))
	roles := map[uint32]string{}
	for _, svc := range spreadServices {
		roles[uint32(s.hosts[svc.host].pids[next[svc.host]])] = svc.role
		next[svc.host]++
	}
	return roles
}

// exportSpreadStore copies each host's ready segments (events and
// methods tables) into <store>/<project>/<host>/, the layout collect
// leaves (internal/ctl collect.go), 0700/0600 as collect makes them.
func exportSpreadStore(t *testing.T, s spreadRun, store string) {
	t.Helper()
	for i, r := range s.hosts {
		name, project := spreadHostNames[i][0], spreadHostNames[i][1]
		src := filepath.Join(r.Dir+segmentsSuffix, project, segment.ReadyDir)
		dst := filepath.Join(store, project, name)
		if err := os.MkdirAll(dst, 0o700); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(src)
		if err != nil || len(entries) == 0 {
			t.Fatalf("host %s: no ready segments in %s (%v)", name, src, err)
		}
		for _, e := range entries {
			data, err := os.ReadFile(filepath.Join(src, e.Name()))
			if err == nil {
				err = os.WriteFile(filepath.Join(dst, e.Name()), data, 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestViewsAcceptance: the spread built by iormeshctl is walkable from
// the mesh — the named services — down to a step's Java stack in show,
// the TUI and the site; the checkout's sequence holds every hop on its
// causing thread with each serving thread's file work interleaved in
// order, every step honestly labeled; the SVG exports are deterministic
// and match their goldens; and the eval harness scores a scripted
// stand-in over the same store end to end.
func TestViewsAcceptance(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	h.JavaAgent = absPath(t, agentJarDefault)
	s := traceSpread(t, h, newMeshRig(t))
	b := newViewsBuild(t, h, s)
	assertNamedMesh(t, b)
	conv := checkoutOf(t, b.model)
	assertCheckoutSequence(t, b, conv)
	t.Run("show", func(t *testing.T) { assertShowWalk(t, b, conv) })
	t.Run("tui", func(t *testing.T) { assertTUIWalk(t, b, conv) })
	t.Run("site", func(t *testing.T) { assertSiteWalk(t, b, conv) })
	t.Run("svg", func(t *testing.T) { assertSVGExports(t, b, conv) })
	t.Run("eval", func(t *testing.T) { assertEvalPipeline(t, b) })
}

// assertNamedMesh: every server of the spread is a named service with
// its fused connections and its scheme, every edge is fused, and the
// merge left nothing unresolved.
func assertNamedMesh(t *testing.T, b *viewsBuild) {
	t.Helper()
	m := b.model
	if m.Run.FusedConns != len(spreadHops) || m.Run.UnresolvedConns != 0 || len(m.Health.BadSegments) != 0 {
		t.Errorf("run: %d fused, %d unresolved, bad segments %v; want %d fused and nothing else",
			m.Run.FusedConns, m.Run.UnresolvedConns, m.Health.BadSegments, len(spreadHops))
	}
	want := map[string]string{"orders": "tls", "inventory": "plain", "billing": "tls"}
	for _, s := range m.Services {
		if tls, ok := want[s.Name]; ok {
			if !s.Named || s.TLS != tls || s.Conns == 0 || s.Unresolved != 0 {
				t.Errorf("service %s: %+v, want named, %s, fused", s.Name, s, tls)
			}
			delete(want, s.Name)
		}
	}
	if len(want) != 0 {
		t.Errorf("services %v missing from %+v", want, m.Services)
	}
	for _, e := range m.Edges {
		if e.State != viewmodel.StateFused {
			t.Errorf("edge %s -> %s is %s", e.From, e.To, e.State)
		}
	}
}

// checkoutOf is the gateway's checkout: the conversation POST /orders
// entered.
func checkoutOf(t *testing.T, m viewmodel.Model) viewmodel.Conversation {
	t.Helper()
	for _, c := range m.Conversations {
		if c.Label == "POST /orders" {
			return c
		}
	}
	t.Fatalf("no POST /orders conversation among %d", len(m.Conversations))
	return viewmodel.Conversation{}
}

// stepIndex is the index of the first step of c whose label contains
// every one of parts, or fails.
func stepIndex(t *testing.T, c viewmodel.Conversation, parts ...string) int {
	t.Helper()
	for i, s := range c.Steps {
		if containsAll(s.Label, parts...) {
			return i
		}
	}
	t.Fatalf("conversation %s has no step %q", c.ID, parts)
	return -1
}

// containsAll reports whether s contains every one of parts.
func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}

// lane names lifeline i as service@host[role/thread]: the role the PID
// is, so the check is independent of the run's PIDs.
func (b *viewsBuild) lane(c viewmodel.Conversation, i int) string {
	if i < 0 || i >= len(c.Lifelines) {
		return fmt.Sprintf("lane %d", i)
	}
	l := c.Lifelines[i]
	return fmt.Sprintf("%s@%s[%s/%s]", l.Service, l.Host, b.roles[l.PID], l.Thread)
}

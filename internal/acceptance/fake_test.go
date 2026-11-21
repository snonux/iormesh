package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"iormesh/internal/schema"
	"iormesh/internal/target"
)

// fakeWorld plays both servers and iormeshctl for the dry run: enough
// state (ready/ files, session states, the uninstall) that every phase
// of the Runner runs its real checks against plausible answers. Nothing
// here contacts a host or runs iormeshctl.
type fakeWorld struct {
	t        *testing.T
	cfg      Config
	mu       sync.Mutex
	remote   []string // "host: argv" per remote command, in order
	ctlCalls []string
	ready    map[readyKey][]string
	seq      int
	ended    map[string]bool // project -> session ended (the watchdog's)
	removed  map[string]bool // host -> uninstalled by rm
	named    bool
	exit1    error // a genuine *exec.ExitError with status 1
	// cert is what base64 prints for a certificate; "" fails the copy.
	cert   string
	copied []string
	// failClient makes that scenario's client role fail, as a client
	// that cannot reach its server does.
	failClient string
	// launched is every workload JVM the run started, with the
	// environment `env` gave it (task kf4).
	launched []launch
}

// launch is one workload JVM: its host, the env assignment it ran under
// ("" without env) and its command line.
type launch struct {
	host, env, cmdline string
}

// answerTimeout runs a workload role: every role must come under
// `timeout 300`, optionally through `env VAR=value` (an agent project's
// JAVA_TOOL_OPTIONS), and a failing client fails with exit status 1.
func (w *fakeWorld) answerTimeout(host string, argv []string) (string, error) {
	if len(argv) < 3 || argv[1] != roleTimeout {
		w.t.Errorf("%s: unexpected timeout command %v", host, argv)
		return "", nil
	}
	java, env := argv[2:], ""
	if java[0] == "env" && len(java) > 1 {
		env, java = java[1], java[2:]
	}
	if len(java) < 6 || java[0] != "java" {
		w.t.Errorf("%s: unexpected timeout command %v", host, argv)
		return "", nil
	}
	w.launched = append(w.launched, launch{host, env, strings.Join(java, " ")})
	if java[3] == w.failClient && java[5] == "client" {
		return "", w.exit1
	}
	w.answerJava(host, java)
	return "", nil
}

func newFakeWorld(t *testing.T, cfg Config) *fakeWorld {
	err := exec.Command("false").Run()
	if exitCode(err) != 1 {
		t.Fatalf("`false` gave %v, want exit status 1", err)
	}
	return &fakeWorld{t: t, cfg: cfg, ready: map[readyKey][]string{}, ended: map[string]bool{},
		removed: map[string]bool{}, exit1: err, cert: "Y2VydA=="}
}

// fakeTransport is one host's view of the world.
type fakeTransport struct {
	w    *fakeWorld
	host string
}

// Run fails like ssh does once ctx is cancelled, so the tests see which
// commands a cancelled run still issues (only those on a cleanup ctx).
func (f fakeTransport) Run(ctx context.Context, argv ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	f.w.remote = append(f.w.remote, f.host+": "+strings.Join(argv, " "))
	out, err := f.w.answer(f.host, argv)
	return []byte(out), err
}

func (f fakeTransport) Fetch(context.Context, string, string) error { return nil }
func (f fakeTransport) Push(context.Context, string, ...string) error {
	return nil
}

// answer is what a server prints for argv.
func (w *fakeWorld) answer(host string, argv []string) (string, error) {
	switch argv[0] {
	case "pwd":
		return "/home/op", nil
	case "uname":
		return map[string]string{"-m": "x86_64", "-r": "7.2.0-test"}[argv[1]], nil
	case "cat":
		return "machine-" + host, nil
	case "getent":
		return map[string]string{"h1": "10.9.0.1 STREAM h1", "h2": "10.9.0.2 STREAM h2"}[argv[2]], nil
	case "test":
		return w.answerTest(host, argv)
	case "sudo":
		return w.answerSudo(host, argv)
	case "timeout":
		return w.answerTimeout(host, argv)
	case "java":
		w.t.Errorf("%s: a workload role without timeout: %v", host, argv)
		return "", nil
	case "ls":
		return w.answerLs(host, argv[2]), nil
	case "ss":
		return "LISTEN 0 50 *:1 *:*", nil
	case "df":
		return "Avail\n5000000000", nil
	case "stat":
		return "2049", nil
	case "fallocate":
		w.ended[projDisk] = true
		return "", nil
	case "base64":
		return w.cert, nil
	case "sh":
		if len(argv) == 6 && strings.Contains(argv[2], "base64 -d") {
			w.copied = append(w.copied, host+":"+argv[5])
		}
	}
	return "", nil // sh -c command -v, systemctl, mkdir, rm, find
}

func (w *fakeWorld) answerTest(host string, argv []string) (string, error) {
	switch {
	case argv[1] == "-d" && argv[2] == traceRoot, argv[1] == "-e" && w.removed[host]:
		return "", w.exit1
	}
	return "", nil
}

func (w *fakeWorld) answerSudo(host string, argv []string) (string, error) {
	if argv[2] == "-l" {
		return installedBin, nil
	}
	proj := argv[len(argv)-1]
	st := `{"project":%q,"state":"running","session":{}}`
	if w.ended[proj] {
		st = `{"project":%q,"state":"ended","session":{"end_reason":"disk_limit"}}`
	}
	return "[" + fmt.Sprintf(st, proj) + "]", nil
}

// answerJava publishes a segment for each project whose pattern matches
// the command line, as the tracer's next rotation would.
func (w *fakeWorld) answerJava(host string, argv []string) {
	for _, p := range w.cfg.projects() {
		g, err := target.CompileGlob(p.pattern)
		if err != nil {
			w.t.Fatal(err)
		}
		if p.host == host && g.Match(strings.Join(argv, " ")) {
			w.seq++
			k := readyKey{host, p.name}
			w.ready[k] = append(w.ready[k], fmt.Sprintf("%s-abcd%04d-20261120T120000Z-%06d.parquet", host, w.seq, w.seq))
		}
	}
}

func (w *fakeWorld) answerLs(host, dir string) string {
	if strings.HasSuffix(dir, "/ready") {
		proj := filepath.Base(filepath.Dir(dir))
		return strings.Join(w.ready[readyKey{host, proj}], "\n")
	}
	return "ready\nstate.json\n.lock\n.active-events.parquet"
}

// fakeCtl plays iormeshctl, writing build outputs into the work dir where
// the real one would.
type fakeCtl struct{ w *fakeWorld }

func (c fakeCtl) Run(ctx context.Context, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	c.w.mu.Lock()
	defer c.w.mu.Unlock()
	c.w.ctlCalls = append(c.w.ctlCalls, strings.Join(args, " "))
	return c.w.ctl(args)
}

func (c fakeCtl) Offline(ctx context.Context, args ...string) (string, string, error) {
	out, err := c.Run(ctx, args...)
	return out, "fake", err
}

func (w *fakeWorld) ctl(args []string) (string, error) {
	switch args[0] {
	case "collect":
		w.collect()
	case "refresh":
		w.collect()
		return "", w.writeBuild(w.meshProjects())
	case "build":
		return "", w.writeBuild(projectArgs(args))
	case "services":
		w.named = w.named || len(args) > 1 && args[1] == "name"
		return "SERVICE " + nameBilling + "\nSERVICE " + nameOrders, nil
	case "show":
		// The summary line viewmodel.WriteSummary prints for a session
		// the watchdog ended.
		return "session    m4disk/" + w.cfg.Host2 + " 1: ended disk_limit (the disk watchdog stopped the tracer)", nil
	case "rm":
		w.removed[args[1]] = true
	case "query":
		return w.query(projectArgs(args)), nil
	}
	return "", nil
}

func projectArgs(args []string) []string {
	var ps []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-project" {
			ps = append(ps, args[i+1])
		}
	}
	slices.Sort(ps)
	return ps
}

func (w *fakeWorld) meshProjects() []string {
	var ps []string
	for _, p := range w.cfg.projects() {
		ps = append(ps, p.name)
	}
	slices.Sort(ps)
	return ps
}

// collect moves every ready file into the store, as rsync would.
func (w *fakeWorld) collect() {
	for k, names := range w.ready {
		dir := filepath.Join(w.cfg.WorkDir, ".local", "share", "iormesh", "traces", k.project, k.host)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			w.t.Fatal(err)
		}
		for _, n := range names {
			if err := os.WriteFile(filepath.Join(dir, n), []byte("seg"), 0o600); err != nil {
				w.t.Fatal(err)
			}
		}
		delete(w.ready, k)
	}
}

// writeBuild writes the model and graph a build of projects gives.
func (w *fakeWorld) writeBuild(projects []string) error {
	dir := filepath.Join(w.cfg.WorkDir, ".local", "share", "iormesh", "builds", strings.Join(projects, "+"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	m, g := w.model(projects), fakeGraph(projects)
	for name, v := range map[string]any{"model.json": m, "graph.json": g} {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// fakeGraph fuses one connection across two hosts for a pair's build, and
// leaves it unresolved for the client's build alone; the spread's build
// fuses its two cross-host hops (fakeSpreadGraph).
func fakeGraph(projects []string) schema.Graph {
	if slices.Equal(projects, spreadNames()) {
		return fakeSpreadGraph()
	}
	g := schema.Graph{SchemaVersion: schema.Version}
	a, b := schema.ProcessNodeID("hA", 1, 1), schema.ProcessNodeID("hB", 2, 1)
	conn := schema.SocketNodeID("c1")
	g.Nodes = []schema.Node{{Type: "process", ID: a}, {Type: "process", ID: b}, {Type: "socket", ID: conn}}
	state := schema.StateFused
	if len(projects) == 1 {
		state = schema.StateUnresolved
	}
	g.Edges = []schema.Edge{{Type: "connected", From: a, To: conn, State: state}}
	if len(projects) > 1 {
		g.Edges = append(g.Edges, schema.Edge{Type: "connected", From: b, To: conn, State: state})
	}
	g.Sort()
	return g
}

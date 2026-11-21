// Package ctl implements iormeshctl, the controller (PLAN.md M4):
//
//	iormeshctl add -project NAME (-pattern GLOB | -listen-port N) [-registry FILE] HOST [TRACER-FLAG...]
//	iormeshctl rm [-project NAME] [-registry FILE] [-store DIR] [-force] [K8S] HOST|POD
//	iormeshctl collect [-project NAME]... [-registry FILE] [-store DIR] [-db FILE] [K8S] [HOST|POD...]
//	iormeshctl deploy [-project NAME] [-registry FILE] [-dist DIR] [-key FILE] [-restart] [K8S]
//	                  [-image IMG] [-attach-timeout D] HOST|POD...
//	iormeshctl build [-project NAME]... [-store DIR] [-db FILE] [-builds DIR] [-routes FILE]
//	                 [-registry FILE]
//	iormeshctl refresh [-project NAME]... [-registry FILE] [-store DIR] [-db FILE] [-builds DIR] [K8S]
//	                   [HOST|POD...]
//	iormeshctl show [summary|services|edges|health] [-project NAME]... [-json] [-unresolved]
//	iormeshctl open [-project NAME]... [-builds DIR]
//	iormeshctl tui [-project NAME]... [-builds DIR]
//	iormeshctl expand [-project NAME]... [-registry FILE] [-store DIR] [-db FILE] [-hops N] [-json]
//	                 [-apply [-dist DIR] [-key FILE]]
//	iormeshctl query [-project NAME]... [-from T] [-to T] [-host GLOB] [-app GLOB] [-thread GLOB]
//	                 [-conn ID [-follow]] [-file GLOB] [-method GLOB] [-max-bytes N]
//	iormeshctl services [list|edges|name|rules|unname] [-db FILE] ...
//	iormeshctl verify [-key FILE] FILE...
//	iormeshctl version
//
// K8S is -context CTX and -namespace NS, and POD a registry pods: entry's
// reference, <cluster>/<namespace>/<pod or selector>: deploy, collect,
// refresh and rm reach pods through the operator's kubectl (pods.go,
// internal/k8s, PLAN.md M7 and decision #18).
//
// add manages the server registry (internal/federate, decisions #12 and
// #21) and rm tears a host's deployment down before unregistering it
// (rm.go); collect moves the servers' published trace segments into the
// local store (collect.go); services reads the service-interaction
// database and edits its naming rules (internal/servicedb, decision #24); verify checks
// release binaries' minisign signatures against the pinned key
// (internal/release, decision #28); deploy verifies the tracer build,
// stages it on registered hosts over the Transport, upgrades the installed
// tracer through the sudoers rule and starts the projects (deploy.go).
// build merges the collected segments offline into the merged graph
// export, the service database and the view model, and refresh is
// collect then build (build.go); show prints the last build's view
// model as text (show.go, internal/viewmodel); open opens the newest
// local site a build wrote (open.go, internal/site); tui explores the same
// model in a terminal (tui.go, internal/tui); query cuts the merged
// trace into a size-capped NDJSON slice (query.go, internal/query). expand
// lists which unresolved peers the next expansion round would enroll and
// why the others stay (expand.go, internal/expand, PLAN.md M6); with
// -apply it enrolls them and runs deploy's steps on them (expandapply.go).
// It is cgo-free (internal/buildgate checks it), so it cross-compiles for
// the darwin and linux laptops it runs on.
package ctl

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"iormesh/internal/federate"
	"iormesh/internal/servicedb"
	"iormesh/internal/termsafe"
	"iormesh/internal/tui"
	"iormesh/internal/version"
	"iormesh/internal/viewmodel"
)

// Exit codes, as the tracer's (internal/cli) uses them.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// errUsage marks a command-line error: reported with usage, exit 2.
var errUsage = errors.New("usage")

// env is what a command needs from its surroundings, injected so tests
// work on a temp registry and never on the operator's.
type env struct {
	stdout, stderr io.Writer
	// registry is the default registry path; -registry overrides it.
	registry func() (string, error)
	// servicedb is the default service database path; -db overrides it.
	servicedb func() (string, error)
	// releaseKey is the default pinned release key; -key overrides it.
	releaseKey func() (string, error)
	// deployed is deploy's record of what it last started.
	deployed func() (string, error)
	// traces is the default local trace store; -store overrides it.
	traces func() (string, error)
	// builds is the default build output directory; -builds overrides it.
	builds func() (string, error)
	// opener hands a site's index.html to the desktop's browser
	// (open.go); nil in tests, which then see the no-opener path.
	opener func(path string) error
	// transport reaches a host: SSH in production, a fake in tests, so
	// no test contacts a server.
	transport func(host string) (federate.Transport, error)
	// terminal reports whether stdin and stdout are a terminal, and tui
	// runs the explorer on it: tests fake both, so no test needs a TTY.
	// A nil terminal is no terminal.
	terminal func() bool
	tui      func(viewmodel.Model) error
}

// defaultEnv is the real environment: the registry at
// federate.DefaultRegistryPath, the database at servicedb.DefaultPath, the
// release key next to the registry, the deploy record, the trace store and
// the build outputs next to the database, SSH to reach hosts and the
// desktop's opener for sites, the TTY check and the Bubble Tea explorer.
func defaultEnv(stdout, stderr io.Writer) env {
	return env{stdout: stdout, stderr: stderr, registry: federate.DefaultRegistryPath,
		servicedb: servicedb.DefaultPath, releaseKey: defaultKeyPath, deployed: defaultDeployedPath,
		traces: defaultStorePath, builds: defaultBuildsPath, transport: sshTransport,
		opener: systemOpener, terminal: stdioIsTerminal, tui: tui.Run}
}

// defaultBuildsPath is ~/.local/share/iormesh/builds, next to the trace
// store and the service database it is built from.
func defaultBuildsPath() (string, error) {
	db, err := servicedb.DefaultPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(db), buildsDirName), nil
}

// sshTransport is the production transport: federate.SSH (decision #12).
func sshTransport(host string) (federate.Transport, error) {
	return federate.NewSSH(host)
}

// Main runs the subcommand args[0] and returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	return run(args, defaultEnv(stdout, stderr))
}

func run(args []string, e env) int {
	if len(args) == 0 {
		usage(e.stderr)
		return exitUsage
	}
	var err error
	switch args[0] {
	case "add":
		err = addCommand(args[1:], e)
	case "rm":
		err = rmCommand(args[1:], e)
	case "collect":
		err = collectCommand(args[1:], e)
	case "deploy":
		err = deployCommand(args[1:], e)
	case "build":
		err = buildCommand(args[1:], e)
	case "refresh":
		err = refreshCommand(args[1:], e)
	case "show":
		err = showCommand(args[1:], e)
	case "open":
		err = openCommand(args[1:], e)
	case "expand":
		err = expandCommand(args[1:], e)
	case "query":
		err = queryCommand(args[1:], e)
	case "tui":
		err = tuiCommand(args[1:], e)
	case checkCommand: // build's child, not for operators (check.go)
		err = checkSegmentCommand(args[1:], e)
	case "services":
		err = servicesCommand(args[1:], e)
	case "routes":
		err = routesCommand(args[1:], e)
	case "verify":
		err = verifyCommand(args[1:], e)
	case "version", "-version", "--version":
		_, _ = fmt.Fprintln(e.stdout, "iormeshctl", version.Stamped()) // keeps version.Stamp linked in
		return exitOK
	case "help", "-h", "--help":
		usage(e.stdout)
		return exitOK
	default:
		err = fmt.Errorf("%w: unknown subcommand %q", errUsage, args[0])
	}
	return exitCodeOf(err, e.stderr)
}

// exitCodeOf maps an error to its exit code.
func exitCodeOf(err error, stderr io.Writer) int {
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, errUsage):
		_, _ = fmt.Fprintf(stderr, "iormeshctl: %s\n", errText(err))
		usage(stderr)
		return exitUsage
	default:
		_, _ = fmt.Fprintf(stderr, "iormeshctl: %s\n", errText(err))
		return exitError
	}
}

// errText is an error as the terminal may see it. Errors carry text the
// servers chose — ssh's and the remote tracer's stderr, host names,
// segment and request names from the traces — so one holding a control
// character is printed Go-quoted (internal/termsafe), never raw.
func errText(err error) string { return termsafe.String(err.Error()) }

func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage:
  iormeshctl add -project NAME (-pattern GLOB | -listen-port N) [-registry FILE] HOST [TRACER-FLAG...]
  iormeshctl rm [-project NAME] [-registry FILE] [-store DIR] [-force] [K8S] HOST|POD
  iormeshctl collect [-project NAME]... [-registry FILE] [-store DIR] [-db FILE] [K8S] [HOST|POD...]
  iormeshctl deploy [-project NAME] [-registry FILE] [-dist DIR] [-key FILE] [-restart]
                    [K8S] [-image IMG] [-attach-timeout D] HOST|POD...
  iormeshctl build [-project NAME]... [-store DIR] [-db FILE] [-builds DIR] [-routes FILE]
                   [-registry FILE]
  iormeshctl refresh [-project NAME]... [-registry FILE] [-store DIR] [-db FILE] [-builds DIR]
                     [-routes FILE] [K8S] [HOST|POD...]
  iormeshctl show [summary|services|edges|health] [-project NAME]... [-builds DIR] [-json] [-unresolved]
  iormeshctl open [-project NAME]... [-builds DIR]
  iormeshctl tui [-project NAME]... [-builds DIR]     (needs a terminal)
  iormeshctl expand [-project NAME]... [-registry FILE] [-store DIR] [-db FILE] [-hops N] [-json]
                    [-apply [-dist DIR] [-key FILE]]
  iormeshctl query [-project NAME]... [-store DIR] [-db FILE] [-from T] [-to T]
                   [-host GLOB] [-app GLOB] [-thread GLOB] [-conn ID [-follow]]
                   [-file GLOB] [-method GLOB] [-max-bytes N]
  iormeshctl services [list] [-db FILE] [-project NAME] [-paths]
  iormeshctl services edges [-db FILE] [-project NAME] [-from NAME]
  iormeshctl services name [-db FILE] [-port N] HOST-GLOB SERVICE
  iormeshctl services rules [-db FILE]
  iormeshctl services unname [-db FILE] RULE-ID
  iormeshctl routes discover [-registry FILE] [-cluster NAME] [-namespace NS]... [-db FILE]
                            [-ingress-service NS/NAME]
  iormeshctl verify [-key FILE] FILE...      (FILE.minisig next to each)
  iormeshctl version

The registry defaults to ~/.config/iormesh/servers.yaml, the pinned
release key to ~/.config/iormesh/minisign.pub, the service database to
~/.local/share/iormesh/services.db, the trace store to
~/.local/share/iormesh/traces (<project>/<host>/), the build outputs to
~/.local/share/iormesh/builds (<project>[+<project>...]/graph.json,
model.json, report.json and sites/<run>/index.html, one site folder per
run, kept until deleted), dist/ to ./dist. open prints the newest site's
path and opens it with xdg-open (open on macOS) when there is one.
deploy only upgrades a tracer already installed on the host (PLAN.md
decision #1). collect without HOST collects every registered host; rm
stops, syncs off and uninstalls before it unregisters, and refuses to
uninstall over traces it could not sync unless -force. build (and
refresh, which is collect then build) merges every project in the store
without -project, never contacts a server, and exits 1 when it had to
leave a damaged segment out. expand lists the unresolved peers of the
store's merged mesh against the registry's expand: allowlist, hop limit
(-hops, default 1) and max_nodes; it contacts no server and deploys
nothing unless -apply, which registers each candidate (one per address,
bound to its listening port) and deploys to it as deploy does; one
-apply is one round: refresh and expand -apply again for the next.
query reads the store as build does and writes the matching
events as schema NDJSON, at most -max-bytes (default 1 MiB) of it; a cut
slice ends in a truncation record. T is RFC 3339 or HH:MM:SS[.frac] UTC
on the trace's first day; -to is exclusive.

POD is a registry pods: entry, <cluster>/<namespace>/<pod> or
<cluster>/<namespace>/<selector>; K8S is -context CTX -namespace NS,
overriding the entry's (-context may only restate its cluster's; rm
and a collect or refresh of every target take neither). A selector
reaches every running pod it matches; each pod is collected into
<project>/pod-<uid>/, pinned in the host pins beside -db. deploy
starts a debug container (iormesh run -linger) in each prepared pod,
or checks a sidecar, with the pinned image; rm collects a pod entry
once more and unregisters it, leaving its tracer running.

The route map (decision #15) is ~/.config/iormesh/routes.yaml (fqdn ->
ingress -> service -> workload, and services with their ClusterIPs and
endpoints), private like the registry; routes discover reads a
registered cluster's Ingresses, Traefik IngressRoutes, Services,
EndpointSlices, pods and ReplicaSets with kubectl get (read-only) into
routes-discovered.json beside the database. build reads both, the file
winning, and prints the routed edges; it never runs kubectl.
`)
}

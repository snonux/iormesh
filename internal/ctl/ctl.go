// Package ctl implements iormeshctl, the controller (docs/milestones.md M4).
// Its usage text lives in one place, the constants of usage.go that usage()
// prints (`iormeshctl -h`), held to every subcommand and flag by
// TestUsageNamesEverySubcommandAndFlag; this comment does not repeat it,
// so the two cannot drift apart (task 2l4). `SUBCMD -h` prints only that
// subcommand's flags and exits 0 (task vh4).
//
// K8S is -context CTX and -namespace NS, and POD a registry pods: entry's
// reference, <cluster>/<namespace>/<pod or selector>: deploy, collect,
// refresh and rm reach pods through the operator's kubectl (pods.go,
// internal/k8s, docs/milestones.md M7 and decision #18).
//
// add manages the server registry (internal/federate, decisions #12 and
// #21) and rm tears a host's deployment down before unregistering it
// (rm.go); collect moves the servers' published trace segments into the
// local store (collect.go); services reads the service-interaction
// database and edits its naming rules (internal/servicedb, decision #24); verify checks
// release binaries' minisign signatures against the pinned key
// (internal/release, decision #28) and sign signs an unpacked release
// with the operator's own key (sign.go, decision #32), which keygen makes
// without a terminal (keygen.go, decision #38); deploy verifies the tracer build,
// stages it on registered hosts over the Transport, upgrades the installed
// tracer through the sudoers rule and starts the projects (deploy.go).
// bootstrap is the first install deploy cannot do: it prints the steps
// that lay down the tracer, the pinned key and the sudoers rule with the
// operator's own sudo, or runs them with -apply (bootstrap.go, decision
// #1). status prints a table of each target's tracer state from the tracers'
// own status reports (status.go), and list prints the registry (list.go).
// ensure restarts the sessions that ended on their own and collects, for
// tracing over days from a timer (ensure.go).
// build merges the collected segments offline into the merged graph
// export, the service database and the view model, and refresh is
// collect then build (build.go; a time window, window.go); prune deletes
// old segments from the store on the operator's word, the service
// database forgetting them first (prune.go); show
// prints the last build's view model as text (show.go, internal/viewmodel); open opens the newest
// local site a build wrote (open.go, internal/site); tui explores the same
// model in a terminal (tui.go, internal/tui); query cuts the merged
// trace into a size-capped NDJSON slice (query.go, internal/query). expand
// lists which unresolved peers the next expansion round would enroll and
// why the others stay (expand.go, internal/expand, docs/milestones.md M6);
// with -apply it enrolls them and runs deploy's steps on them
// (expandapply.go). It is cgo-free (internal/buildgate checks it), so it
// cross-compiles for the darwin and linux laptops it runs on.
package ctl

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

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
	// now is the clock build -last and prune -older-than count back
	// from (window.go); nil is time.Now. Tests fix it.
	now func() time.Time
	// checked, when not nil, is called with each segment file before
	// the child decodes it (build.go checkedSegmentsIn): tests count
	// the segments a command opens. Nil outside tests.
	checked func(path string)
	// hostTimeout bounds everything one command does on one host
	// (onHost); zero is defaultHostTimeout. Tests shorten it.
	hostTimeout time.Duration
	// ctx is the root context every command runs under (root): Main's
	// is cancelled by SIGINT or SIGTERM (interruptContext), so a Ctrl-C
	// ends the command's ssh and kubectl calls and it cleans up and
	// reports "interrupted" (task pi4). Nil is context.Background(), as
	// in most tests.
	ctx context.Context
}

// root is the command's root context, e.ctx or context.Background().
func (e env) root() context.Context {
	if e.ctx == nil {
		return context.Background()
	}
	return e.ctx
}

// clock is the current time, by e.now or time.Now.
func (e env) clock() time.Time {
	if e.now == nil {
		return time.Now()
	}
	return e.now()
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

// defaultHostTimeout is the ceiling on the time one command (collect,
// deploy, rm, and refresh's and expand's collect and deploy) spends on
// one host before it gives up on it and moves to the next. The cheaper
// bounds come first and catch the usual hangs: ssh's ConnectTimeout
// and ServerAlive options a dead link within about a minute, rsync's
// --timeout a transfer that stops moving data (federate.SSH). What is
// left for this ceiling is a remote command that hangs on a live
// connection — a sudo or find stuck on a dead NFS mount. It is an hour,
// not minutes, because a collect after a long outage may move a large
// ready/ backlog over a slow link, and cutting that off would only
// delay it: --remove-source-files deletes just what arrived, so the
// rest waits for the next run either way.
const defaultHostTimeout = time.Hour

// onHost runs one host's work under a deadline derived from ctx, the
// caller's (a cancelled ctx stops it too). A failure that is the
// deadline's says so, with the bound, so the operator can tell a
// stalled host from a refused one.
func (e env) onHost(ctx context.Context, work func(context.Context) error) error {
	timeout := e.hostTimeout
	if timeout <= 0 {
		timeout = defaultHostTimeout
	}
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := work(hctx)
	if err != nil && ctx.Err() == nil && errors.Is(hctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("%w (gave up on the host after %s)", err, timeout)
	}
	return err
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

// Main runs the subcommand args[0] under a root context that SIGINT and
// SIGTERM cancel (interruptContext) and returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	ctx, stop := interruptContext()
	defer stop()
	e := defaultEnv(stdout, stderr)
	e.ctx = ctx
	return run(args, e)
}

// interruptContext is the root context a Ctrl-C (SIGINT) or a kill
// (SIGTERM) cancels, with the signal as its cause, instead of killing
// iormeshctl outright (task pi4): the running command's ssh and kubectl
// calls end, its deferred cleanups run (the fetch lock, kubectl debug's
// --custom temporary) and it reports "interrupted". Only the first
// signal is caught: once the context is cancelled the handler is
// dropped, so a second Ctrl-C kills a command whose cleanup hangs.
func interruptContext() (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	context.AfterFunc(ctx, stop)
	return ctx, stop
}

// commands are the subcommands run dispatches to, by name: a table, not
// a switch, so TestUsageNamesEverySubcommandAndFlag holds the usage text
// to every subcommand there is (task vh4). version and help only print,
// so run answers them itself.
var commands = map[string]func([]string, env) error{
	"add":        addCommand,
	"rm":         rmCommand,
	"collect":    collectCommand,
	"deploy":     deployCommand,
	"bootstrap":  bootstrapCommand,
	"status":     statusCommand,
	"ensure":     ensureCommand,
	"list":       listCommand,
	"build":      buildCommand,
	"refresh":    refreshCommand,
	"show":       showCommand,
	"open":       openCommand,
	"expand":     expandCommand,
	"query":      queryCommand,
	"prune":      pruneCommand,
	"tui":        tuiCommand,
	checkCommand: checkSegmentCommand, // build's child, not for operators (check.go)
	"services":   servicesCommand,
	"routes":     routesCommand,
	"verify":     verifyCommand,
	"sign":       signCommand,
	"keygen":     keygenCommand,
}

func run(args []string, e env) int {
	if len(args) == 0 {
		usage(e.stderr)
		return exitUsage
	}
	switch args[0] {
	case "version", "-version", "--version":
		_, _ = fmt.Fprintln(e.stdout, "iormeshctl", safe(version.Stamped())) // keeps version.Stamp linked in
		return exitOK
	case "help", "-h", "--help":
		usage(e.stdout)
		return exitOK
	}
	cmd, ok := commands[args[0]]
	if !ok {
		return exitCodeOf(fmt.Errorf("%w: unknown subcommand %q", errUsage, args[0]), e.stderr)
	}
	return exitCodeOf(interrupted(e.root(), cmd(args[1:], e)), e.stderr)
}

// interrupted marks a command's failure as the interrupt's once the
// root context has ended (Ctrl-C, SIGTERM): the error then is mostly
// the calls it cut off, and the operator should read why first.
func interrupted(ctx context.Context, err error) error {
	if err == nil || ctx.Err() == nil {
		return err
	}
	return fmt.Errorf("interrupted (%v): %w", context.Cause(ctx), err)
}

// exitCodeOf maps an error to its exit code. `SUBCMD -h` is a request,
// not a mistake (task vh4): the flag set has printed that subcommand's
// help, so flag.ErrHelp exits 0 with nothing added — no "flag: help
// requested" line, no full usage. Every flag parse wraps its error with
// %w for this.
func exitCodeOf(err error, stderr io.Writer) int {
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
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

// errLines is a host's error as deploy, ensure and expand -apply print it
// on a line of its own (task dm4): the first line as errText would show
// it, every further line — a failed tracer unit's journal tail, which
// the remote start appends (internal/cli startdiag.go) — indented
// beneath it, each line escaped on its own (termsafe.Lines). errText
// would quote the whole multi-line error into one line of \n escapes,
// burying the journal line that says why the unit failed. Kept for
// prints that end their line with it; a table cell (status) keeps
// errText.
func errLines(err error) string { return termsafe.Lines(err.Error(), "  ") }

// safe is text as the terminal may see it: unchanged when printable,
// Go-quoted when it holds a control character (internal/termsafe).
// Hostnames, pod names, contexts, paths and service names come from
// servers, clusters, traces and files that may be compromised, and
// printed raw they could rewrite the operator's terminal. Every fmt print
// in this package passes each text value through safe, errText,
// errLines or termsafe itself — TestCtlPrintsAreTerminalSafe in internal/buildgate
// fails on one that does not (task oj4) — so no print has to decide
// whether its value could be hostile. Wrapping text already safe, or
// built from safe parts, changes nothing: String leaves printable text,
// its own quoted output included, as it is.
func safe(s string) string { return termsafe.String(s) }

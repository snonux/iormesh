// Package ctl implements iormeshctl, the controller (PLAN.md M4):
//
//	iormeshctl add -project NAME (-pattern GLOB | -listen-port N) [-registry FILE] HOST [TRACER-FLAG...]
//	iormeshctl rm [-project NAME] [-registry FILE] HOST
//	iormeshctl services [list|edges|name|rules|unname] [-db FILE] ...
//
// add and rm manage the server registry (internal/federate, decisions #12
// and #21); services reads the service-interaction database and edits
// its naming rules (internal/servicedb, decision #24). deploy, collect,
// build and refresh follow in later M4 tasks.
// It is cgo-free (internal/buildgate checks it), so it cross-compiles for
// the darwin and linux laptops it runs on.
package ctl

import (
	"errors"
	"fmt"
	"io"

	"iormesh/internal/federate"
	"iormesh/internal/servicedb"
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
}

// defaultEnv is the real environment: the registry at
// federate.DefaultRegistryPath, the database at servicedb.DefaultPath.
func defaultEnv(stdout, stderr io.Writer) env {
	return env{stdout: stdout, stderr: stderr, registry: federate.DefaultRegistryPath,
		servicedb: servicedb.DefaultPath}
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
	case "services":
		err = servicesCommand(args[1:], e)
	case "help", "-h", "--help":
		usage(e.stdout)
		return exitOK
	default:
		err = fmt.Errorf("%w: unknown subcommand %q", errUsage, args[0])
	}
	return report(err, e.stderr)
}

// report maps an error to its exit code.
func report(err error, stderr io.Writer) int {
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, errUsage):
		_, _ = fmt.Fprintf(stderr, "iormeshctl: %v\n", err)
		usage(stderr)
		return exitUsage
	default:
		_, _ = fmt.Fprintf(stderr, "iormeshctl: %v\n", err)
		return exitError
	}
}

func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage:
  iormeshctl add -project NAME (-pattern GLOB | -listen-port N) [-registry FILE] HOST [TRACER-FLAG...]
  iormeshctl rm [-project NAME] [-registry FILE] HOST
  iormeshctl services [list] [-db FILE] [-project NAME] [-paths]
  iormeshctl services edges [-db FILE] [-project NAME] [-from NAME]
  iormeshctl services name [-db FILE] [-port N] HOST-GLOB SERVICE
  iormeshctl services rules [-db FILE]
  iormeshctl services unname [-db FILE] RULE-ID

The registry defaults to ~/.config/iormesh/servers.yaml, the service
database to ~/.local/share/iormesh/services.db.
`)
}

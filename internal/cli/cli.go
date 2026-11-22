// Package cli implements the iormesh tracer's subcommands (M1 step 10):
//
//	iormesh run (-pid PID[,PID…] | -pattern GLOB | -listen-port N [-listen-grace 2m]) -project NAME -out DIR [-duration 4h] [-ready FILE] [-cpuprofile FILE]
//	iormesh normalize [-bracket S] [-tmp PREFIX] [-check expect.json] [-out DIR] TRACEDIR
//	iormesh attach -pid PID -agent JAR -spool DIR [-timeout 10s]
//	iormesh start [run's flags] [-cpu-quota 100%] [-memory-max 1G] [-wait 2m]
//	iormesh stop -project NAME [-trace-dir DIR]
//	iormesh status [-project NAME] [-trace-dir DIR] [-json]
//	iormesh upgrade [-allow-downgrade] FILE
//	iormesh uninstall
//	iormesh ready -project NAME [-state | -status | -remove -- FILE...]
//	iormesh version
//
// run is the foreground tracer; its target spec (internal/target) is a
// PID list, an argv/exe glob matched on exec, or a listening port (M2
// steps 1–3, session.go). M4 wraps it (start.go, control.go): start runs
// it as the transient systemd unit iormesh-<project>, stop and status
// control and read that unit and the run's state.json, uninstall stops
// every unit and removes /opt/iormesh (decision #19) and then the sudoers
// rule iormeshctl bootstrap installed (decision #1), and upgrade
// installs a staged build only once its minisign signature verifies
// against the root-owned pinned key (upgrade.go, decisions #1 and #28).
// normalize applies the golden-check library (internal/normalize) to a
// written trace. ready streams a project's published segments as a tar
// (or removes fetched ones), the k8s transport's fetch (ready.go,
// internal/k8s; no tar or rsync in the tracer image). attach live-loads
// the Java agent into a running JVM a `run -spool` traces (decision #2,
// task ge4;
// `run -agent` does the same for the JVMs a session starts with and,
// as they appear, later ones, task lh4).
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"iormesh/internal/version"
)

// Exit codes. A failed golden check is its own code so a script can tell
// "the trace is wrong" from "the command could not run".
const (
	exitOK         = 0
	exitError      = 1
	exitUsage      = 2
	exitViolations = 3
)

// Output file names inside a trace directory (docs/schema.md).
const (
	EventsFile = "events.ndjson"
	GraphFile  = "graph.json"
	// StacksFile is the native stack table, written only by a
	// -native-stacks run (M2 step 9).
	StacksFile = "stacks.ndjson"
	// MethodsFile and ThreadsFile are the Java method and thread
	// tables, written only by a -spool run (M3, task 2b4).
	MethodsFile = "methods.ndjson"
	ThreadsFile = "threads.ndjson"
	// ClassesFile is the Java class table (the class-load map), written
	// only by a -spool run (M3, task fe4).
	ClassesFile = "classes.ndjson"
)

// errUsage marks a command-line error: reported with usage, exit 2.
var errUsage = errors.New("usage")

// Main runs the subcommand args[0] and returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}
	// Through the one sudoers rule only the lifecycle subcommands run
	// (confine.go): fail closed for every other, present or future.
	if err := sudoCommandGate(args[0]); err != nil {
		return report(err, exitOK, stderr)
	}
	var code int
	var err error
	switch args[0] {
	case "run":
		err = runCommand(args[1:], stderr)
	case "normalize":
		code, err = normalizeCommand(args[1:], stdout, stderr)
	case "attach":
		err = attachCommand(args[1:], stderr)
	case "start":
		err = controlCommand(control.start, args[1:], stdout, stderr)
	case "stop":
		err = controlCommand(control.stop, args[1:], stdout, stderr)
	case "status":
		err = controlCommand(control.status, args[1:], stdout, stderr)
	case "uninstall":
		err = controlCommand(control.uninstall, args[1:], stdout, stderr)
	case "upgrade":
		err = upgradeCommand(args[1:], stdout, stderr)
	case "ready":
		err = readyCommand(args[1:], stdout, stderr)
	case "version", "-version", "--version":
		_, _ = fmt.Fprintln(stdout, "iormesh", version.Stamped()) // keeps version.Stamp linked in
		return exitOK
	case "help", "-h", "--help":
		usage(stdout)
		return exitOK
	default:
		err = fmt.Errorf("%w: unknown subcommand %q", errUsage, args[0])
	}
	return report(err, code, stderr)
}

// report maps an error to its exit code; code is the command's own result
// when it ran to completion. `SUBCMD -h` is a request, not a mistake
// (task vh4): the flag set has printed that subcommand's help, so
// flag.ErrHelp exits 0 with nothing added — no "flag: help requested"
// line, no full usage. Every flag parse wraps its error with %w for this.
func report(err error, code int, stderr io.Writer) int {
	switch {
	case err == nil:
		return code
	case errors.Is(err, flag.ErrHelp):
		return exitOK
	case errors.Is(err, errUsage):
		_, _ = fmt.Fprintf(stderr, "iormesh: %v\n", err)
		usage(stderr)
		return exitUsage
	default:
		_, _ = fmt.Fprintf(stderr, "iormesh: %v\n", err)
		return exitError
	}
}

func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage:
  iormesh run (-pid PID[,PID...] | -pattern GLOB | -listen-port N [-listen-grace 2m])
              -project NAME (in a pod default: its namespace)
              [-out DIR] [-trace-dir DIR (default /opt/iormesh/traces without -out)]
              [-disk-limit 90] [-disk-min-free 256MiB] [-disk-max-bytes 0] [-ready-owner UID:GID]
              [-duration 4h] [-ready FILE] [-trace-families ipc,signal,poll,zerocopy|all]
              [-cpuprofile FILE] [-aggregate=false] [-native-stacks] [-spool DIR [-agent JAR]]
  iormesh normalize [-bracket SCENARIO] [-tmp PREFIX] [-check expect.json] [-out DIR] TRACEDIR
  iormesh attach -pid PID -agent JAR -spool DIR [-timeout 10s]
  iormesh start [run's flags] [-cpu-quota 100%] [-memory-max 1G] [-wait 2m]
  iormesh stop -project NAME [-trace-dir DIR]
  iormesh status [-project NAME] [-trace-dir DIR] [-json]
  iormesh upgrade [-allow-downgrade] FILE    (FILE.minisig next to it)
  iormesh uninstall
  iormesh ready -project NAME [-state | -status | -remove -- FILE...]   (k8s: segments as a tar, the attach state or the status JSON)
  iormesh version
`)
}

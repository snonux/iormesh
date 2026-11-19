// Package cli implements the iormesh tracer's subcommands (M1 step 10):
//
//	iormesh run (-pid PID[,PID…] | -pattern GLOB | -listen-port N) -project NAME -out DIR [-duration 4h] [-ready FILE] [-cpuprofile FILE]
//	iormesh normalize [-bracket S] [-tmp PREFIX] [-check expect.json] [-out DIR] TRACEDIR
//
// run is the foreground tracer; its target spec (internal/target) is a
// PID list, an argv/exe glob matched on exec, or a listening port (M2
// steps 1–3, session.go). M4 adds start/stop/status/upgrade/uninstall
// around it. normalize applies the golden-check library
// (internal/normalize) to a written trace.
package cli

import (
	"errors"
	"fmt"
	"io"
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
	var code int
	var err error
	switch args[0] {
	case "run":
		err = runCommand(args[1:], stderr)
	case "normalize":
		code, err = normalizeCommand(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		usage(stdout)
		return exitOK
	default:
		err = fmt.Errorf("%w: unknown subcommand %q", errUsage, args[0])
	}
	return report(err, code, stderr)
}

// report maps an error to its exit code; code is the command's own result
// when it ran to completion.
func report(err error, code int, stderr io.Writer) int {
	switch {
	case err == nil:
		return code
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
  iormesh run (-pid PID[,PID...] | -pattern GLOB | -listen-port N) -project NAME -out DIR
              [-duration 4h] [-ready FILE] [-trace-families ipc,signal,poll,zerocopy|all]
              [-cpuprofile FILE] [-aggregate=false] [-native-stacks]
  iormesh normalize [-bracket SCENARIO] [-tmp PREFIX] [-check expect.json] [-out DIR] TRACEDIR
`)
}

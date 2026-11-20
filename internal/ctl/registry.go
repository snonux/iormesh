package ctl

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"iormesh/internal/federate"
)

// ownedFlags are tracer flags the registry entry or deploy sets itself:
// the project label, the target and the trace directory. Passed through as
// well they would contradict the entry — or, since Go's flag parsing stops
// at HOST, be an iormeshctl flag the operator put after the host by
// mistake — so add refuses them instead of storing them.
var ownedFlags = map[string]bool{
	"project": true, "pattern": true, "listen-port": true, "pid": true,
	"out": true, "registry": true,
}

// addCommand registers a project on a host: iormeshctl add -project NAME
// (-pattern GLOB | -listen-port N) HOST [TRACER-FLAG...]. Only the
// registry file changes; nothing contacts the host until deploy.
func addCommand(args []string, e env) error {
	fs := newFlagSet("add", e.stderr)
	name := fs.String("project", "", "project label (required, decision #21)")
	pattern := fs.String("pattern", "", "glob matched against the target's command line")
	port := fs.Uint("listen-port", 0, "trace whoever listens on this TCP port")
	path := fs.String("registry", "", "registry file (default ~/.config/iormesh/servers.yaml)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("%w: add needs a HOST", errUsage)
	}
	if *port > 65535 {
		return fmt.Errorf("%w: -listen-port %d: out of range", errUsage, *port)
	}
	host, flags := fs.Arg(0), tracerFlags(fs.Args()[1:])
	if err := checkTracerFlags(flags); err != nil {
		return err
	}
	p := federate.Project{Name: *name, Flags: flags,
		Target: federate.Target{Pattern: *pattern, ListenPort: uint16(*port)}}
	return editRegistry(*path, e, func(r *federate.Registry) error {
		return r.Add(host, p)
	}, fmt.Sprintf("added %s project %s", host, *name))
}

// rmCommand unregisters a host, or one project on it with -project. For
// now it edits the registry only: syncing the host's traces off and
// uninstalling the tracer (PLAN.md M4 "Cleanup (rm)") come with collect.
func rmCommand(args []string, e env) error {
	fs := newFlagSet("rm", e.stderr)
	name := fs.String("project", "", "remove only this project (default: the whole host)")
	path := fs.String("registry", "", "registry file (default ~/.config/iormesh/servers.yaml)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%w: rm takes exactly one HOST", errUsage)
	}
	host := fs.Arg(0)
	what := "removed " + host
	if *name != "" {
		what += " project " + *name
	}
	return editRegistry(*path, e, func(r *federate.Registry) error {
		return r.Remove(host, *name)
	}, what)
}

// editRegistry loads the registry, applies edit and saves it, then reports
// done on stdout. A failed edit leaves the file untouched.
func editRegistry(path string, e env, edit func(*federate.Registry) error, done string) error {
	if path == "" {
		var err error
		if path, err = e.registry(); err != nil {
			return err
		}
	}
	r, err := federate.LoadRegistry(path)
	if err != nil {
		return err
	}
	if err := edit(&r); err != nil {
		return err
	}
	if err := r.Save(path); err != nil {
		return err
	}
	_, err = fmt.Fprintf(e.stdout, "%s (%s)\n", done, path)
	return err
}

// tracerFlags drops the "--" an operator may put between HOST and the
// tracer flags.
func tracerFlags(rest []string) []string {
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	if len(rest) == 0 {
		return nil
	}
	return rest
}

// checkTracerFlags refuses ownedFlags in either spelling (-x, --x, -x=v).
func checkTracerFlags(flags []string) error {
	for _, f := range flags {
		if !strings.HasPrefix(f, "-") {
			continue
		}
		key, _, _ := strings.Cut(strings.TrimLeft(f, "-"), "=")
		if ownedFlags[key] {
			return fmt.Errorf("%w: %s after HOST: iormeshctl flags go before HOST, "+
				"and the registry entry sets the tracer's", errUsage, f)
		}
	}
	return nil
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

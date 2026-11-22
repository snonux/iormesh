package ctl

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"iormesh/internal/federate"
)

// `iormeshctl list [-registry FILE]` (task ph4) prints the registry: one
// row per host and project, then per pod entry and project, with what the
// project traces and the tracer flags deploy passes through. It reads the
// registry file only and contacts nothing; `iormeshctl status` asks the
// tracers. The registry is private to the operator, but a hand-edited or
// expand-enrolled entry still reaches the terminal through safe.

// listCommand parses list's flags and prints the registry's targets.
func listCommand(args []string, e env) error {
	fs := newFlagSet("list", e.stderr)
	regPath := fs.String("registry", "", "registry file (default ~/.config/iormesh/servers.yaml)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: list takes no arguments", errUsage)
	}
	r, err := loadRegistry(*regPath, e)
	if err != nil {
		return err
	}
	if len(r.Servers) == 0 && len(r.Pods) == 0 {
		_, err := fmt.Fprintln(e.stdout, "no hosts or pods registered")
		return err
	}
	return printRegistry(e.stdout, r)
}

// printRegistry prints r's hosts, then its pod entries, as an aligned
// table. A host enrolled by expand shows its hop; a pod entry its mode
// and the container it traces.
func printRegistry(w io.Writer, r federate.Registry) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "KIND\tTARGET\tPROJECT\tTRACES\tFLAGS\tNOTE")
	for _, s := range r.Servers {
		note := "-"
		if s.Hop > 0 {
			note = "hop " + strconv.Itoa(s.Hop)
		}
		for _, p := range s.Projects {
			_, _ = fmt.Fprintf(tw, "host\t%s\t%s\t%s\t%s\t%s\n", safe(s.Host), safe(p.Name),
				safe(tracesText(p.Target)), safe(flagsText(p.Flags)), safe(note))
		}
	}
	for _, pod := range r.Pods {
		note := r.PodMode(pod) + " mode, container " + pod.Container
		for _, p := range r.PodProjects(pod) {
			_, _ = fmt.Fprintf(tw, "pod\t%s\t%s\t%s\t%s\t%s\n", safe(r.PodRef(pod)), safe(p.Name),
				safe(tracesText(p.Target)), safe(flagsText(p.Flags)), safe(note))
		}
	}
	return tw.Flush()
}

// tracesText is a project's target as the table shows it.
func tracesText(t federate.Target) string {
	if t.ListenPort != 0 {
		return "listen-port " + strconv.Itoa(int(t.ListenPort))
	}
	return "pattern " + t.Pattern
}

// flagsText is the pass-through flags, "-" for none. An element holding
// a space or nothing is quoted, so the columns still say where each one
// ends.
func flagsText(flags []string) string {
	if len(flags) == 0 {
		return "-"
	}
	out := make([]string, len(flags))
	for i, f := range flags {
		out[i] = f
		if f == "" || strings.ContainsAny(f, " \t") {
			out[i] = strconv.Quote(f)
		}
	}
	return strings.Join(out, " ")
}

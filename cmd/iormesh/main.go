// Command iormesh is the tracer: it runs on a traced host, loads the BPF
// probes and writes the trace (PLAN.md, M1). All logic lives in
// internal/cli; main only dispatches, so the subcommands stay testable.
package main

import (
	"os"

	"iormesh/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}

// Command iormeshctl is the controller (PLAN.md M4): it runs on the
// operator's laptop, Linux or macOS, with no eBPF and no cgo, and drives
// the tracers on registered servers over SSH. All logic lives in
// internal/ctl; main only dispatches, so the subcommands stay testable.
package main

import (
	"os"

	"iormesh/internal/ctl"
)

func main() {
	os.Exit(ctl.Main(os.Args[1:], os.Stdout, os.Stderr))
}

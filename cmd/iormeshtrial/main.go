// Command iormeshtrial runs M8's agent trial (docs/milestones/m8.md,
// decision #37): one agent in one mode of the iormesh skill against the
// throwaway VMs, with a scripted user, scored like the M5 eval. `mage
// agentTrial` runs it; it is never part of `mage test`. All logic lives
// in internal/agenttrial.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"iormesh/internal/agenttrial"
	"iormesh/internal/version"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "-version" {
		fmt.Println(version.Version)
		return
	}
	o, err := agenttrial.ParseFlags(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		os.Exit(2)
	}
	if err == nil {
		err = agenttrial.Main(context.Background(), o, os.Stdout)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "iormeshtrial: %v\n", err)
		os.Exit(1)
	}
}

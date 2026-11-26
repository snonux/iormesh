// Command iormeshtrial runs M8's agent trial (docs/milestones/m8.md,
// decision #37): one agent in one mode of the iormesh skill against the
// throwaway VMs, with a scripted user, scored like the M5 eval. `mage
// agentTrial` runs it; it is never part of `mage test`. All logic,
// the flags and the exit codes included, lives in internal/agenttrial
// (Command).
package main

import (
	"context"
	"os"

	"iormesh/internal/agenttrial"
)

func main() {
	os.Exit(agenttrial.Command(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

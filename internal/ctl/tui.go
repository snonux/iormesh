package ctl

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/term"
)

// `iormeshctl tui` (PLAN.md M5; docs/visualization.md "The terminal
// UI"): the Bubble Tea explorer over the last build's model.json — the
// same file `show` reads, through the same loadModel, so the TUI is no
// second reading of the data.

// errNoTerminal is tui run without a terminal on stdin and stdout: a
// full-screen UI drawn into a pipe or a file is garbage, and one reading
// keys from a pipe never ends, so it refuses and points at show.
var errNoTerminal = errors.New("tui needs a terminal on stdin and stdout; " +
	"for text use iormeshctl show (it pipes)")

// tuiCommand opens the explorer over the build of -project.
func tuiCommand(args []string, e env) error {
	fs := newFlagSet("tui", e.stderr)
	var f buildFlags
	addReadBuildFlags(fs, &f)
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: tui takes no arguments", errUsage)
	}
	if e.terminal == nil || !e.terminal() {
		return errNoTerminal
	}
	m, err := loadModel(e, f)
	if err != nil {
		return err
	}
	if e.tui == nil { // an env built without one (a test): nothing to draw on
		return errNoTerminal
	}
	return e.tui(m)
}

// stdioIsTerminal reports whether both stdin (the keys) and stdout (the
// frames) are terminals.
func stdioIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

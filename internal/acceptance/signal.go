package acceptance

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// InterruptContext returns a context that the first SIGINT or SIGTERM
// cancels; Run then goes on to its teardown (cleanupCtx, up to 5
// minutes). A second signal exits the process at once with 128+signo,
// the teardown unfinished (the runbook's manual cleanup). The returned
// func stops listening.
//
// mage's generated main also listens for SIGINT and, 5 seconds after
// it, exits with "cleanup timeout exceeded" whether or not the target
// returned, which would cut the teardown off and leave sessions tracing
// on the hosts (task mi4). signal.Ignore first undoes every earlier
// Notify for SIGINT, mage's included, so only this process's own
// channel sees the signal and the teardown runs here, with its results
// table. Call it after mage has started the target (m4Acceptance does,
// after its builds); a SIGINT in the instant between the two calls is
// lost. The outer `mage` process ignores SIGINT and waits for this one.
//
// The channel stays registered until the returned func runs: dropping
// it after the first signal would not bring back the default handling,
// since Go then restores the SIG_IGN that Ignore set, which the
// teardown's iormeshctl and ssh would inherit; hence the explicit exit
// on the second signal.
func InterruptContext(parent context.Context) (context.Context, context.CancelFunc) {
	signal.Ignore(syscall.SIGINT)
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(parent)
	quit := make(chan struct{})
	go watchInterrupts(ch, cancel, quit)
	var once sync.Once
	return ctx, func() {
		once.Do(func() { signal.Stop(ch); close(quit) })
		cancel()
	}
}

// watchInterrupts cancels the run on the first signal and exits the
// process on the second, until quit is closed.
func watchInterrupts(ch <-chan os.Signal, cancel context.CancelFunc, quit <-chan struct{}) {
	for first := true; ; first = false {
		var sig os.Signal
		select {
		case <-quit:
			return
		case sig = <-ch:
		}
		if first {
			cancel()
			continue
		}
		fmt.Fprintf(os.Stderr, "%v again: exiting, the teardown unfinished"+
			" (manual cleanup: docs/testing.md, M4 acceptance)\n", sig)
		code := 1
		if s, ok := sig.(syscall.Signal); ok {
			code = 128 + int(s)
		}
		os.Exit(code)
	}
}

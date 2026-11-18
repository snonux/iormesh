//go:build mage

// WorkloadTest checks for M2's restart and non-java-peer scenarios (task
// oa4): their usage and run-failure invocations sit in Magefile.go's
// lists; the checks here need a prepared directory or the Go peer itself.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// m2AppUsageErrors are malformed restart and non-java-peer invocations:
// both run solo; restart takes only --dir, non-java-peer --dir and --peer.
var m2AppUsageErrors = [][]string{
	{"restart", "--role", "client"},
	{"restart", "--peer", "x"},
	{"non-java-peer", "--role", "server"},
	{"non-java-peer", "--port", "1"},
}

// m2AppRunFailures fail at run time (exit 1): no --dir, and an explicit
// --peer that is not an executable (a skip would hide a broken harness).
var m2AppRunFailures = [][]string{
	{"restart", "--dir", "/nonexistent/iormesh-workload"},
	{"non-java-peer", "--dir", "/nonexistent/iormesh-workload"},
	{"non-java-peer", "--peer", "/nonexistent/echopeer"},
}

// checkRestartPeer runs the checks below in order.
func checkRestartPeer() error {
	for _, check := range []func() error{checkRestartStaleState, checkPeerSkip,
		checkEchoPeerUsage} {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

// checkRestartStaleState: a state file left in --dir would turn the first
// incarnation into a "second" one, so restart must refuse it (exit 1)
// rather than run a bracket its golden cannot match.
func checkRestartStaleState() error {
	dir, err := os.MkdirTemp("", "iormesh-restart-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.WriteFile(filepath.Join(dir, "restart.state"), []byte("incarn-1"), 0o644); err != nil {
		return err
	}
	return expectExit(1, "restart", "--dir", dir)
}

// checkPeerSkip: a jar with no echopeer beside it has nothing to talk to,
// so non-java-peer skips (exit 3) instead of failing.
func checkPeerSkip() error {
	dir, err := os.MkdirTemp("", "iormesh-peerless-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	jar, err := os.ReadFile(workloadJar)
	if err != nil {
		return err
	}
	lonely := filepath.Join(dir, workloadJar)
	if err := os.WriteFile(lonely, jar, 0o644); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gateWait)
	defer cancel()
	err = exec.CommandContext(ctx, "java", "-jar", lonely, "non-java-peer").Run()
	return wantExitCode(err, 3, "non-java-peer without a peer binary")
}

// checkEchoPeerUsage: the peer rejects a missing -dir and stray
// arguments with the usage code 2, like the workload does.
func checkEchoPeerUsage() error {
	for _, args := range [][]string{{}, {"-dir", "x", "stray"}, {"-bogus"}} {
		ctx, cancel := context.WithTimeout(context.Background(), gateWait)
		err := exec.CommandContext(ctx, "./"+echoPeerBinary, args...).Run()
		cancel()
		if err := wantExitCode(err, 2, fmt.Sprintf("echopeer %q", args)); err != nil {
			return err
		}
	}
	return nil
}

// wantExitCode fails unless err is an exit with code.
func wantExitCode(err error, code int, what string) error {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != code {
		return fmt.Errorf("%s: want exit %d, got %v", what, code, err)
	}
	return nil
}

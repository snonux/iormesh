//go:build mage

// Magefile for iormesh: the Java workload targets (M0); their process
// harness is in magefile_workload.go. The tracer build, BPF and
// generation targets lifted from ior live beside it in magefile_bpf.go,
// magefile_generate.go and magefile_fmt.go (docs/implementation.md,
// "M1 — walking skeleton", step 1).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
)

const (
	workloadSrcDir     = "workload/src"
	workloadClassesDir = "workload/classes"
	workloadJar        = "workload.jar"
	workloadMainClass  = "iormesh.workload.Main"
	// The workload targets Java 21 (PLAN.md decision #5) whatever newer
	// JDK builds it, so it never depends on post-21 APIs.
	workloadRelease = "21"
)

// WorkloadBuild compiles the Java workload with the JDK alone (no Maven or
// Gradle, so the traced app has no build-time dependencies) and bundles
// each scenario's expect.json so `--expect <name>` can print it.
func WorkloadBuild() error {
	srcs, err := javaSources(workloadSrcDir)
	if err != nil {
		return err
	}
	// A clean class dir keeps classes of deleted sources out of the jar.
	if err := os.RemoveAll(workloadClassesDir); err != nil {
		return err
	}
	args := append([]string{"--release", workloadRelease, "-Xlint:all", "-Werror",
		"-d", workloadClassesDir}, srcs...)
	if err := sh.RunV("javac", args...); err != nil {
		return err
	}
	return sh.RunV("jar", "--create", "--file", workloadJar,
		"--main-class", workloadMainClass,
		"-C", workloadClassesDir, ".",
		"-C", "workload", "scenarios")
}

// javaSources lists every .java file under root, sorted by WalkDir.
func javaSources(root string) ([]string, error) {
	var srcs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".java") {
			srcs = append(srcs, path)
		}
		return nil
	})
	if err == nil && len(srcs) == 0 {
		err = fmt.Errorf("no java sources under %s", root)
	}
	return srcs, err
}

// WorkloadTest runs every scenario without a tracer (--self-test), then
// checks the gates, the two-process tcp-loopback run, every scenario's
// --expect output and the exit codes of bad invocations.
func WorkloadTest() error {
	mg.Deps(WorkloadBuild)
	if err := sh.RunV("java", "-jar", workloadJar, "--self-test"); err != nil {
		return err
	}
	checks := []func() error{
		checkGatedRun,
		checkTCPLoopback,
		checkExpectations,
		checkUsageErrors,
		checkRunFailures,
	}
	for _, check := range checks {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

// checkGatedRun proves the harness gates hold: the PID comes first on
// stdout, no marker fires while the startup file is missing, and the
// process outlives its scenario until the hold file appears.
func checkGatedRun() error {
	dir, err := os.MkdirTemp("", "iormesh-gate-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	startup, hold := filepath.Join(dir, "startup"), filepath.Join(dir, "hold")

	run, err := startGated(startup, hold)
	if err != nil {
		return err
	}
	defer run.kill()
	if err := run.expectPID(); err != nil {
		return err
	}
	run.drainStdout()
	if err := run.expectQuiet("before the startup file", time.Second); err != nil {
		return err
	}
	if err := touch(startup); err != nil {
		return err
	}
	if err := run.expectStderr("marker noop/end"); err != nil {
		return err
	}
	if err := run.expectQuiet("before the hold file", 500*time.Millisecond); err != nil {
		return err
	}
	if err := touch(hold); err != nil {
		return err
	}
	return run.wait()
}

// checkTCPLoopback runs tcp-loopback as the harness will in M1: a server
// on a kernel-picked port (the default --port 0), then a client started
// only after the server's `listening <port>` line, so the connect never
// races the listen. Both must finish their bracket and exit 0.
func checkTCPLoopback() error {
	server, err := startRun(nil, "tcp-loopback", "--role", "server")
	if err != nil {
		return err
	}
	defer server.kill()
	if err := server.expectPID(); err != nil {
		return err
	}
	port, err := server.expectListening()
	if err != nil {
		return err
	}
	server.drainStdout()
	client, err := startRun(nil, "tcp-loopback", "--role", "client", "--port", fmt.Sprint(port))
	if err != nil {
		return err
	}
	defer client.kill()
	if err := client.expectPID(); err != nil {
		return err
	}
	client.drainStdout()
	// Client first: a server stuck in accept would otherwise hide the
	// client failure that caused it behind a timeout.
	if err := finish(client, "tcp-loopback"); err != nil {
		return err
	}
	return finish(server, "tcp-loopback")
}

// finish expects the scenario's end marker on stderr, then exit code 0.
func finish(r *gatedRun, scenario string) error {
	if err := r.expectStderr("marker " + scenario + "/end"); err != nil {
		return err
	}
	return r.wait()
}

// checkExpectations asserts --expect prints the bundled expect.json as
// valid JSON naming its scenario, for every scenario --list names, so a
// new scenario without an expect.json fails here rather than in M1.
func checkExpectations() error {
	out, err := runBounded("--list")
	if err != nil {
		return fmt.Errorf("--list: %w", err)
	}
	names := strings.Fields(string(out))
	if len(names) == 0 {
		return errors.New("--list: no scenarios")
	}
	for _, name := range names {
		if err := checkExpectation(name); err != nil {
			return err
		}
	}
	return nil
}

// checkExpectation asserts `--expect name` prints valid JSON whose
// scenario field is name.
func checkExpectation(name string) error {
	out, err := runBounded("--expect", name)
	if err != nil {
		return fmt.Errorf("--expect %s: %w", name, err)
	}
	var exp struct {
		Scenario string `json:"scenario"`
	}
	if err := json.Unmarshal(out, &exp); err != nil {
		return fmt.Errorf("--expect %s: invalid JSON: %w", name, err)
	}
	if exp.Scenario != name {
		return fmt.Errorf("--expect %s: scenario %q", name, exp.Scenario)
	}
	return nil
}

// checkUsageErrors asserts each malformed invocation exits with the usage
// code 2, before any scenario I/O.
func checkUsageErrors() error {
	bad := [][]string{
		{},
		{"no-such-scenario"},
		{"noop", "--role", "bogus"},
		{"noop", "--role"},
		{"noop", "--role", "solo", "--role", "client"},
		{"--self-test", "extra"},
		{"--list", "extra"},
		{"noop", "stray"},
		{"--expect"},
		{"--expect", "no-such-scenario"},
		// Scenario-level option checks (Options.java).
		{"tcp-loopback", "--role", "client"},
		{"tcp-loopback", "--role", "client", "--port", "abc"},
		{"tcp-loopback", "--role", "client", "--port", "70000"},
		{"tcp-loopback", "--role", "client", "--port", "0"},
		{"tcp-loopback", "--role", "solo"},
		{"file-basic", "--role", "server"},
		{"file-basic", "--bogus", "x"},
	}
	for _, args := range bad {
		if err := expectExit(2, args...); err != nil {
			return err
		}
	}
	return nil
}

// checkRunFailures asserts a well-formed run that fails at run time exits
// with code 1, not the usage code: a client with nobody listening, and a
// --dir that does not exist.
func checkRunFailures() error {
	port, err := closedPort()
	if err != nil {
		return err
	}
	failing := [][]string{
		{"tcp-loopback", "--role", "client", "--port", fmt.Sprint(port)},
		{"file-basic", "--dir", "/nonexistent/iormesh-workload"},
	}
	for _, args := range failing {
		if err := expectExit(1, args...); err != nil {
			return err
		}
	}
	return nil
}

// closedPort returns a loopback port nothing listens on: one the kernel
// just handed out and that was closed again. The kernel picks such ports
// from the whole ephemeral range, so another process taking this one in
// the second before the client connects is unlikely enough for a check.
func closedPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	return port, l.Close()
}

// expectExit runs the workload jar and fails unless it exits with code.
func expectExit(code int, args ...string) error {
	_, err := runBounded(args...)
	var exitErr *exec.ExitError
	switch {
	case err == nil && code == 0:
		return nil
	case errors.As(err, &exitErr) && exitErr.ExitCode() == code:
		return nil
	default:
		return fmt.Errorf("workload %q: want exit %d, got %v", args, code, err)
	}
}

// runBounded runs the workload jar to completion and returns its stdout.
// gateWait bounds it, so a run that blocks (say, a client whose "closed"
// port got reused by a silent listener) fails the check instead of
// hanging it.
func runBounded(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gateWait)
	defer cancel()
	return workloadCmd(ctx, args...).Output()
}

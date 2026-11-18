//go:build mage

// Magefile for iormesh. M0 only needs the Java workload targets; the
// tracer build and BPF generation targets are lifted from ior in M1
// (docs/implementation.md, "M1 — walking skeleton", step 1).
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
// checks the command line rejects bad input with the usage exit code.
func WorkloadTest() error {
	mg.Deps(WorkloadBuild)
	if err := sh.RunV("java", "-jar", workloadJar, "--self-test"); err != nil {
		return err
	}
	if err := checkGatedRun(); err != nil {
		return err
	}
	if err := checkExpectation(); err != nil {
		return err
	}
	return checkUsageErrors()
}

// gateWait bounds every wait in the gated-run check; the JVM starts in
// well under a second, so this only stops a hung check.
const gateWait = 20 * time.Second

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

// gatedRun is a workload process whose stdout and stderr are read line by
// line, and whose exit is reported on done.
type gatedRun struct {
	cmd    *exec.Cmd
	outR   io.Reader
	stdout *bufio.Scanner
	stderr chan string
	done   chan error
}

func startGated(startup, hold string) (*gatedRun, error) {
	cmd := exec.Command("java", "-jar", workloadJar, "noop")
	cmd.Env = append(os.Environ(),
		"IORMESH_WORKLOAD_STARTUP_FILE="+startup,
		"IORMESH_WORKLOAD_HOLD_FILE="+hold)
	// io.Pipe writers instead of Cmd.StdoutPipe/StderrPipe: Wait then
	// drains the output itself, so reaping the process can never cut a
	// read short (StdoutPipe forbids Wait before all reads complete).
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	cmd.Stdout, cmd.Stderr = outW, errW
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	r := &gatedRun{cmd: cmd, outR: outR, stdout: bufio.NewScanner(outR),
		stderr: make(chan string, 64), done: make(chan error, 1)}
	go func() {
		err := cmd.Wait()
		_ = outW.Close()
		_ = errW.Close()
		r.done <- err
	}()
	go func() {
		sc := bufio.NewScanner(errR)
		for sc.Scan() {
			r.stderr <- sc.Text()
		}
		close(r.stderr)
	}()
	return r, nil
}

func (r *gatedRun) expectPID() error {
	if !r.stdout.Scan() {
		return fmt.Errorf("gated run: no stdout line: %v", r.stdout.Err())
	}
	if got := r.stdout.Text(); got != fmt.Sprint(r.cmd.Process.Pid) {
		return fmt.Errorf("first stdout line %q is not the PID %d", got, r.cmd.Process.Pid)
	}
	// Stdout after the PID line is not checked; drain it (only now, so
	// the drain cannot race the scanner for the PID) so the copy
	// goroutine behind Wait never blocks.
	go func() { _, _ = io.Copy(io.Discard, r.outR) }()
	return nil
}

// expectQuiet fails if the process prints to stderr or exits within d.
func (r *gatedRun) expectQuiet(phase string, d time.Duration) error {
	select {
	case line, ok := <-r.stderr:
		if ok {
			return fmt.Errorf("gated run: %q printed %s", line, phase)
		}
		return fmt.Errorf("gated run: stderr closed %s", phase)
	case err := <-r.done:
		return fmt.Errorf("gated run exited %s: %v", phase, err)
	case <-time.After(d):
		return nil
	}
}

// expectStderr waits until want appears as a stderr line.
func (r *gatedRun) expectStderr(want string) error {
	timeout := time.After(gateWait)
	for {
		select {
		case line, ok := <-r.stderr:
			if !ok {
				return fmt.Errorf("gated run: stderr closed before %q", want)
			}
			if line == want {
				return nil
			}
		case <-timeout:
			return fmt.Errorf("gated run: no %q within %v", want, gateWait)
		}
	}
}

func (r *gatedRun) wait() error {
	select {
	case err := <-r.done:
		if err != nil {
			return fmt.Errorf("gated run: %w", err)
		}
		return nil
	case <-time.After(gateWait):
		return fmt.Errorf("gated run: no exit within %v of the hold file", gateWait)
	}
}

// kill ends a run left behind by a failed check; harmless after exit.
func (r *gatedRun) kill() { _ = r.cmd.Process.Kill() }

func touch(path string) error {
	return os.WriteFile(path, nil, 0o600)
}

// checkExpectation asserts --expect prints the bundled expect.json as
// valid JSON naming its scenario.
func checkExpectation() error {
	out, err := exec.Command("java", "-jar", workloadJar, "--expect", "noop").Output()
	if err != nil {
		return fmt.Errorf("--expect noop: %w", err)
	}
	var exp struct {
		Scenario string `json:"scenario"`
	}
	if err := json.Unmarshal(out, &exp); err != nil {
		return fmt.Errorf("--expect noop: invalid JSON: %w", err)
	}
	if exp.Scenario != "noop" {
		return fmt.Errorf("--expect noop: scenario %q", exp.Scenario)
	}
	return nil
}

// checkUsageErrors asserts each malformed invocation exits with code 2.
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
	}
	for _, args := range bad {
		if err := expectExit(2, args...); err != nil {
			return err
		}
	}
	return nil
}

// expectExit runs the workload jar and fails unless it exits with code.
func expectExit(code int, args ...string) error {
	cmd := exec.Command("java", append([]string{"-jar", workloadJar}, args...)...)
	err := cmd.Run()
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

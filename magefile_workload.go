//go:build mage

// The process harness behind WorkloadTest (Magefile.go): it starts the
// workload jar and reads its stdout and stderr line by line, so a check
// can assert on the PID-first contract, a server's `listening <addr>`
// line and the scenario markers while the process still runs.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// gateWait bounds every wait in the workload checks; the JVM starts in
// well under a second, so this only stops a hung check.
const gateWait = 20 * time.Second

// gatedRun is a workload process whose stdout and stderr are read line by
// line, and whose exit is reported on done.
//
// Both streams are read by goroutines into channels, so every wait can be
// bounded by gateWait with a select — a blocking Scan on a pipe could not
// be, and a hung workload would hang `mage workloadTest` instead of
// failing it. The channels hold 64 lines; the scenarios print two or
// three, and drainStdout empties stdout once a check has read its lines.
type gatedRun struct {
	cmd    *exec.Cmd
	stdout chan string
	stderr chan string
	done   chan error
}

// startGated runs the noop scenario behind the startup and hold gates.
func startGated(startup, hold string) (*gatedRun, error) {
	return startRun([]string{
		"IORMESH_WORKLOAD_STARTUP_FILE=" + startup,
		"IORMESH_WORKLOAD_HOLD_FILE=" + hold,
	}, "noop")
}

// startRun starts the workload jar with args, adding env to the inherited
// environment.
func startRun(env []string, args ...string) (*gatedRun, error) {
	cmd := workloadCmd(context.Background(), args...)
	cmd.Env = append(os.Environ(), env...)
	// io.Pipe writers instead of Cmd.StdoutPipe/StderrPipe: Wait then
	// drains the output itself, so reaping the process can never cut a
	// read short (StdoutPipe forbids Wait before all reads complete).
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	cmd.Stdout, cmd.Stderr = outW, errW
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	r := &gatedRun{cmd: cmd, stdout: make(chan string, 64),
		stderr: make(chan string, 64), done: make(chan error, 1)}
	go func() {
		err := cmd.Wait()
		_ = outW.Close()
		_ = errW.Close()
		r.done <- err
	}()
	go readLines(outR, r.stdout)
	go readLines(errR, r.stderr)
	return r, nil
}

// readLines sends each line of in to lines and closes lines at EOF.
func readLines(in io.Reader, lines chan<- string) {
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		lines <- sc.Text()
	}
	close(lines)
}

// workloadCmd builds `java -jar workload.jar args...`; ctx bounds runs
// that are not driven through a gatedRun.
func workloadCmd(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "java", append([]string{"-jar", workloadJar}, args...)...)
}

// name labels a run in error messages by its workload arguments (the
// ones after "java -jar workload.jar").
func (r *gatedRun) name() string {
	return fmt.Sprintf("workload %q", r.cmd.Args[3:])
}

// nextStdout returns the next stdout line, failing if stdout ends or no
// line arrives within gateWait.
func (r *gatedRun) nextStdout() (string, error) {
	select {
	case line, ok := <-r.stdout:
		if !ok {
			return "", fmt.Errorf("%s: stdout ended early", r.name())
		}
		return line, nil
	case <-time.After(gateWait):
		return "", fmt.Errorf("%s: no stdout line within %v", r.name(), gateWait)
	}
}

// expectPID asserts the first stdout line is the process's PID.
func (r *gatedRun) expectPID() error {
	got, err := r.nextStdout()
	if err != nil {
		return err
	}
	if got != fmt.Sprint(r.cmd.Process.Pid) {
		return fmt.Errorf("%s: first stdout line %q is not the PID %d",
			r.name(), got, r.cmd.Process.Pid)
	}
	return nil
}

// expectListening reads a server's `listening <addr>` line and returns
// addr: the port the kernel picked (tcp-loopback, udp-loopback) or the
// socket path (unix-socket); the caller validates its shape.
func (r *gatedRun) expectListening() (string, error) {
	line, err := r.nextStdout()
	if err != nil {
		return "", err
	}
	addr, ok := strings.CutPrefix(line, "listening ")
	if !ok || addr == "" {
		return "", fmt.Errorf("%s: want `listening <addr>`, got %q", r.name(), line)
	}
	return addr, nil
}

// drainStdout discards the rest of stdout so the reader goroutine (and
// the copy behind Wait) never blocks on a full channel. Called only after
// the lines a check reads, so the drain cannot steal them.
func (r *gatedRun) drainStdout() {
	go func() {
		for range r.stdout {
		}
	}()
}

// expectQuiet fails if the process prints to stderr or exits within d.
func (r *gatedRun) expectQuiet(phase string, d time.Duration) error {
	select {
	case line, ok := <-r.stderr:
		if ok {
			return fmt.Errorf("%s: %q printed %s", r.name(), line, phase)
		}
		return fmt.Errorf("%s: stderr closed %s", r.name(), phase)
	case err := <-r.done:
		return fmt.Errorf("%s exited %s: %v", r.name(), phase, err)
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
				return fmt.Errorf("%s: stderr closed before %q", r.name(), want)
			}
			if line == want {
				return nil
			}
		case <-timeout:
			return fmt.Errorf("%s: no %q within %v", r.name(), want, gateWait)
		}
	}
}

// wait expects the process to exit with code 0 within gateWait.
func (r *gatedRun) wait() error {
	select {
	case err := <-r.done:
		if err != nil {
			return fmt.Errorf("%s: %w", r.name(), err)
		}
		return nil
	case <-time.After(gateWait):
		return fmt.Errorf("%s: no exit within %v", r.name(), gateWait)
	}
}

// kill ends a run left behind by a failed check; harmless after exit.
func (r *gatedRun) kill() { _ = r.cmd.Process.Kill() }

// touch creates an empty file, releasing a workload gate.
func touch(path string) error {
	return os.WriteFile(path, nil, 0o600)
}

package integrationtests

// Adapted from ior integrationtests/harness.go @ b21afc50328c
// (OutputCapture, readWorkloadPID, killAndReap, stopAndDrainTimer) and
// iormesh's magefile_workload.go (gatedRun). Changes: one process type for
// the workload and the tracer, whose stdout lines arrive on a channel and
// whose exit is reported on a channel, so every wait is a bounded select;
// io.Pipe writers instead of StdoutPipe, so reaping a process never cuts a
// read short.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// stdoutLines is the stdout channel's capacity. The workload prints its
// PID, the server its "listening" line and every scenario two marker
// lines; the room is many times that, so the reader never blocks the
// process on a line nobody waits for.
const stdoutLines = 256

// OutputCapture is a goroutine-safe sink: os/exec copies stderr in its own
// goroutine while a failed wait already reads it for the error message.
type OutputCapture struct {
	mu  sync.Mutex
	buf strings.Builder
}

// process is one started child (workload or tracer).
type process struct {
	name   string
	cmd    *exec.Cmd
	stdout chan string
	stderr *OutputCapture
	done   chan struct{} // closed once the process was reaped
	err    error         // its Wait result; valid after done
}

// Write appends p; it never fails.
func (c *OutputCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

// String returns everything captured so far.
func (c *OutputCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// startProcess starts cmd with stdout read line by line and stderr teed to
// the test binary's stderr (visible in -test.v output) and to a capture.
func startProcess(name string, cmd *exec.Cmd) (*process, error) {
	p := &process{name: name, cmd: cmd, stdout: make(chan string, stdoutLines),
		stderr: &OutputCapture{}, done: make(chan struct{})}
	outR, outW := io.Pipe()
	cmd.Stdout = outW
	cmd.Stderr = io.MultiWriter(os.Stderr, p.stderr)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	go func() {
		p.err = cmd.Wait()
		_ = outW.Close()
		close(p.done)
	}()
	go readLines(outR, p.stdout)
	return p, nil
}

// readLines sends each line of in to lines and closes lines at EOF. A line
// that finds the channel full is dropped rather than blocking the child.
func readLines(in io.Reader, lines chan<- string) {
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		select {
		case lines <- sc.Text():
		default:
		}
	}
	_, _ = io.Copy(io.Discard, in) // after an over-long line: keep draining
	close(lines)
}

// waitLine returns the first stdout line starting with prefix. Lines before
// it are consumed; the child ending its output, or the timeout, is an
// error that carries its stderr.
func (p *process) waitLine(prefix string, timeout time.Duration) (string, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case line, ok := <-p.stdout:
			if !ok {
				return "", p.failure(fmt.Sprintf("ended its output before a %q line", prefix))
			}
			if strings.HasPrefix(line, prefix) {
				return line, nil
			}
		case <-timer.C:
			return "", p.failure(fmt.Sprintf("printed no %q line within %v", prefix, timeout))
		}
	}
}

// wait waits for the child to exit; a non-zero exit or the timeout (which
// kills it) is an error carrying its stderr.
func (p *process) wait(timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-p.done:
		if p.err != nil {
			return p.failure(p.err.Error())
		}
		return nil
	case <-timer.C:
		p.kill()
		return p.failure(fmt.Sprintf("did not exit within %v", timeout))
	}
}

// exited reports whether the child has been reaped.
func (p *process) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// kill kills the child unless it already exited, and reaps it.
func (p *process) kill() {
	if p.exited() {
		return
	}
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return // nothing to reap: it was never ours to kill
	}
	<-p.done
}

func (p *process) failure(why string) error {
	stderr := strings.TrimSpace(p.stderr.String())
	if stderr == "" {
		return fmt.Errorf("%s %s", p.name, why)
	}
	return fmt.Errorf("%s %s; stderr:\n%s", p.name, why, stderr)
}

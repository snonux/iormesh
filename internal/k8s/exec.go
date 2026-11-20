package k8s

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// runner runs one kubectl invocation. Kubectl takes it as a field so
// tests can count calls; the tests that check argv run the real
// execRunner against a fake kubectl on PATH.
type runner interface {
	// output runs kubectl args and returns its stdout.
	output(ctx context.Context, args []string) ([]byte, error)
	// stream runs kubectl args with its stdout handed to consume, so a
	// fetch of many segments never sits in memory whole.
	stream(ctx context.Context, args []string, consume func(io.Reader) error) error
}

// stderrLimit bounds how much of a failed kubectl's stderr an error
// carries: enough for its diagnosis, not a remote log dump.
const stderrLimit = 2048

// waitDelay bounds Wait once kubectl has exited or been killed, should a
// child (an exec credential plugin) keep the pipes open.
const waitDelay = 2 * time.Second

// execRunner is the real runner: the kubectl binary bin, found on PATH.
// stdin is /dev/null, so nothing ever reads the operator's terminal.
type execRunner struct {
	bin string
}

func (e execRunner) output(ctx context.Context, args []string) ([]byte, error) {
	var stdout bytes.Buffer
	cmd, stderr := e.command(ctx, args)
	cmd.Stdout = &stdout
	// Run first: the operands of a return are evaluated left to right.
	err := explain(cmd.Run(), stderr)
	return stdout.Bytes(), err
}

// stream starts kubectl with its stdout on a pipe for consume. A consumer
// that fails cancels kubectl, so a refused stream is never drained to the
// end; its error wins over the kill's.
func (e execRunner) stream(ctx context.Context, args []string, consume func(io.Reader) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd, stderr := e.command(ctx, args)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	cerr := consume(out)
	if cerr != nil {
		cancel()
	} else {
		// Drain what the consumer left (tar's trailing padding), so
		// kubectl never blocks on a full pipe.
		_, _ = io.Copy(io.Discard, out)
	}
	werr := explain(cmd.Wait(), stderr)
	if cerr != nil {
		return cerr
	}
	return werr
}

// command builds the exec.Cmd with stderr captured.
func (e execRunner) command(ctx context.Context, args []string) (*exec.Cmd, *bytes.Buffer) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, e.bin, args...)
	cmd.Stderr = &stderr
	cmd.WaitDelay = waitDelay
	return cmd, &stderr
}

// explain adds the start of stderr to an exit error, where kubectl says
// what went wrong (forbidden, not found, unknown context).
func explain(err error, stderr *bytes.Buffer) error {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return err
	}
	msg := strings.TrimSpace(stderr.String())
	if len(msg) > stderrLimit {
		msg = msg[:stderrLimit] + "…"
	}
	return fmt.Errorf("%w: %s", err, msg)
}

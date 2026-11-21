package k8s

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// runner runs one kubectl invocation. Kubectl takes it as a field so a
// test could stand in for kubectl; the tests run the real execRunner
// against a fake kubectl on PATH, so they see the exact argv.
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

// stderrTail is how much of stderr's end is kept beside its head: the
// line kubectl exec ends with when the remote command failed.
const stderrTail = 512

// drainLimit bounds what stream reads after the consumer is done: a tar
// ends with at most a record's zero padding (10 KiB by default), so
// anything beyond this is not a tar the tracer wrote.
const drainLimit = 64 << 10

// waitDelay bounds Wait once kubectl has exited or been killed, should a
// child (an exec credential plugin) keep the pipes open.
const waitDelay = 2 * time.Second

// remoteExit is the last stderr line kubectl exec writes when the remote
// command ran and exited non-zero; kubectl then exits with that status.
var remoteExit = regexp.MustCompile(`command terminated with exit code ([0-9]+)$`)

// execRunner is the real runner: the kubectl binary bin, found on PATH.
// stdin is /dev/null, so nothing ever reads the operator's terminal.
type execRunner struct {
	bin string
}

func (e execRunner) output(ctx context.Context, args []string) ([]byte, error) {
	stdout := &capBuffer{limit: outputLimit}
	cmd, stderr := e.command(ctx, args)
	cmd.Stdout = stdout
	// Run first: the operands of a return are evaluated left to right.
	err := explain(cmd.Run(), stderr)
	if stdout.over {
		return nil, fmt.Errorf("kubectl %s: output over %d bytes", strings.Join(args, " "), outputLimit)
	}
	return stdout.Bytes(), err
}

// outputLimit bounds what output holds of one kubectl's stdout: a pod's
// JSON is kilobytes, a namespace's route-discovery list (internal/k8s
// RouteReader) a few MiB; past this the answer is not one to hold in
// memory, and the copy fails, which ends kubectl.
const outputLimit = 128 << 20

// capBuffer is a bytes.Buffer refusing writes past limit.
type capBuffer struct {
	bytes.Buffer
	limit int
	over  bool
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if c.Len()+len(p) > c.limit {
		c.over = true
		return 0, errors.New("output limit reached")
	}
	return c.Buffer.Write(p)
}

// stream starts kubectl with its stdout on a pipe for consume. A consumer
// that fails cancels kubectl, so a refused stream is never drained to the
// end; its error wins over the kill's. After a consumer that succeeded,
// at most drainLimit more bytes are read (the tar's trailing padding);
// a stream that goes on is cancelled and refused.
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
	if cerr == nil {
		if n, _ := io.CopyN(io.Discard, out, drainLimit); n == drainLimit {
			cerr = fmt.Errorf("%w: more than %d bytes after the tar's end", ErrStream, drainLimit)
		}
	}
	if cerr != nil {
		cancel()
	}
	werr := explain(cmd.Wait(), stderr)
	if cerr != nil {
		return cerr
	}
	return werr
}

// customProfileGate opens `kubectl debug --custom` on kubectl 1.30, where
// the flag exists only behind this variable (checked on kind, task yb4:
// 1.30.14 says "unknown flag: --custom" without it; 1.31 and later have
// it on by default and ignore the variable; 1.29 lacks the flag either
// way). Set on every call: it changes nothing but debug's flag set.
const customProfileGate = "KUBECTL_DEBUG_CUSTOM_PROFILE=true"

// command builds the exec.Cmd with stderr captured, bounded, and the
// operator's environment (KUBECONFIG, credential plugins) plus
// customProfileGate.
func (e execRunner) command(ctx context.Context, args []string) (*exec.Cmd, *headTail) {
	stderr := &headTail{}
	cmd := exec.CommandContext(ctx, e.bin, args...)
	cmd.Env = append(os.Environ(), customProfileGate)
	cmd.Stderr = stderr
	cmd.WaitDelay = waitDelay
	return cmd, stderr
}

// explain tells a remote command's failure from kubectl's own. Both exit
// non-zero, kubectl's own (forbidden, pod not found, container not
// running, unknown context) mostly with 1 — the very status `test` uses
// for "no". Only when stderr ends with kubectl exec's "command terminated
// with exit code N" line, N being kubectl's status, did the command run:
// that error wraps *exec.ExitError, as federate.Transport promises. Any
// other failure wraps ErrUnreachable and not the ExitError, so no caller
// reads an unreached pod as a "no" from it; the kubectlFailure beside it
// keeps kubectl's own words for a caller that is not about a pod
// (RouteReader.Get). Either way the start of stderr says what went
// wrong.
func explain(err error, stderr *headTail) error {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return err
	}
	msg := stderr.head()
	if n, ok := stderr.remoteStatus(); ok && n == exitErr.ExitCode() {
		return fmt.Errorf("%w: %s", err, msg)
	}
	return fmt.Errorf("%w: %w", ErrUnreachable, &kubectlFailure{status: err.Error(), stderr: msg})
}

// kubectlFailure is kubectl's own failure: its exit status and the start
// of its stderr, as text — it must not wrap the *exec.ExitError (see
// explain).
type kubectlFailure struct {
	status, stderr string
}

func (e *kubectlFailure) Error() string {
	return fmt.Sprintf("kubectl %s: %s", e.status, e.stderr)
}

// headTail is an io.Writer keeping the first stderrLimit and the last
// stderrTail bytes of what it was given, so a chatty remote command
// cannot grow the controller's memory and the closing line survives.
type headTail struct {
	first []byte
	last  []byte
	more  bool // bytes were dropped between first and last
}

func (h *headTail) Write(p []byte) (int, error) {
	n := len(p)
	if room := stderrLimit - len(h.first); room > 0 {
		k := min(room, len(p))
		h.first = append(h.first, p[:k]...)
		p = p[k:]
	}
	h.last = append(h.last, p...)
	if extra := len(h.last) - stderrTail; extra > 0 {
		h.last = h.last[extra:]
		h.more = true
	}
	return n, nil
}

// head is stderr's start for an error message.
func (h *headTail) head() string {
	msg := strings.TrimSpace(string(h.first))
	if len(h.last) > 0 {
		msg += "…"
	}
	return msg
}

// remoteStatus parses kubectl exec's closing line from stderr's end.
func (h *headTail) remoteStatus() (int, bool) {
	all := string(h.first)
	if h.more {
		all = string(h.last)
	} else {
		all += string(h.last)
	}
	all = strings.TrimSpace(all)
	last := all[strings.LastIndexByte(all, '\n')+1:]
	m := remoteExit.FindStringSubmatch(last)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

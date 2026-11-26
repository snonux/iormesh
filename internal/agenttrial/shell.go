package agenttrial

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

// Defaults of the VM shell.
const (
	// DefaultCommandTimeout bounds one command on a VM: long enough for a
	// package install or a deploy, short enough that a hung command cannot
	// eat the run's hour.
	DefaultCommandTimeout = 10 * time.Minute
	// DefaultOutputBytes caps one command's output handed back to the agent,
	// as eval's DefaultSliceBytes caps a query slice.
	DefaultOutputBytes = 32 << 10
)

// Who ran a command, in the command log.
const (
	ByAgent   = "agent"   // automatic mode: the agent's RUN
	ByUser    = "user"    // guided mode: the scripted user typed it
	ByHarness = "harness" // preparation and the mesh check
)

// Shell is the only way anything reaches a VM, for the agent and for the
// harness alike (decision #37, "Where": the agent's only shell is SSH
// into the VMs). It runs `ssh <options> -- <host> <command>` with no
// local shell in between: the host must be one of Hosts, exactly, and the
// command travels as ssh's single remote-command argument, so whatever
// it holds — `;`, `$(...)`, backquotes, redirections, a laptop path — is
// interpreted by the VM's shell, never by the laptop's. Every call is
// logged before its result is returned.
type Shell struct {
	// SSH is the ssh binary; Options go before the "--" (e.g. "-F", a
	// config naming the VMs).
	SSH     string
	Options []string
	// Hosts are the names a command may go to: the VMs, nothing else.
	Hosts    []string
	Timeout  time.Duration
	MaxBytes int
	// Log receives one JSON line per command (CommandLog).
	Log io.Writer

	mu sync.Mutex
}

// Call is one command for Shell.Run.
type Call struct {
	Host, Command string
	// By and Step label the log entry: who ran it and under which
	// approved explanation (0: none, the harness's own).
	By   string
	Step int
	// Stdin feeds the remote command (the harness copying a file); nil
	// gives it /dev/null.
	Stdin io.Reader
	// LogAs replaces the command in the log (a command carrying a secret).
	LogAs string
}

// CommandLog is one line of the command log.
type CommandLog struct {
	Time    string `json:"time"`
	By      string `json:"by"`
	Step    int    `json:"step,omitempty"`
	Host    string `json:"host"`
	Command string `json:"command"`
	Exit    int    `json:"exit"`
	Error   string `json:"error,omitempty"`
}

// ErrRefused is a call the shell will not send: a host that is no VM, or
// a command that cannot be one argument.
var ErrRefused = errors.New("refused")

// Check refuses a call before anything runs: the host must be one of
// Hosts exactly (so neither "localhost", a path nor an "-o" option can
// stand in for it), the command non-empty and free of NUL bytes.
func (s *Shell) Check(host, command string) error {
	switch {
	case !slices.Contains(s.Hosts, host):
		return fmt.Errorf("%w: host %q is not one of the trial's VMs %v", ErrRefused, host, s.Hosts)
	case strings.TrimSpace(command) == "":
		return fmt.Errorf("%w: empty command", ErrRefused)
	case strings.ContainsRune(command, 0):
		return fmt.Errorf("%w: a NUL byte in the command", ErrRefused)
	}
	return nil
}

// Argv is the ssh command line of a call: BatchMode so nothing prompts on
// the laptop, no escape character, no tty, and the host and the command
// after "--" so neither can be taken for an option.
func (s *Shell) Argv(host, command string) []string {
	argv := append([]string{cmp.Or(s.SSH, "ssh")}, s.Options...)
	return append(argv, "-T", "-e", "none", "-o", "BatchMode=yes", "--", host, command)
}

// Run runs one call and returns its output (stdout and stderr merged, in
// the order written, capped) and its exit status. A refused call runs
// nothing and returns ErrRefused; a failing command is not an error, its
// status is the caller's to read.
func (s *Shell) Run(ctx context.Context, c Call) (string, int, error) {
	if err := s.Check(c.Host, c.Command); err != nil {
		s.log(c, -1, err)
		return "", -1, err
	}
	ctx, cancel := context.WithTimeout(ctx, cmp.Or(s.Timeout, DefaultCommandTimeout))
	defer cancel()
	argv := s.Argv(c.Host, c.Command)
	var out bytes.Buffer
	w := &capped{w: &out, n: int64(cmp.Or(s.MaxBytes, DefaultOutputBytes))}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = w, w, c.Stdin
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit) && ctx.Err() == nil:
		code, err = exit.ExitCode(), nil
	case err != nil:
		code = -1
		err = fmt.Errorf("ssh %s: %w", c.Host, cmp.Or(ctx.Err(), err))
	}
	if w.cut {
		out.WriteString(fmt.Sprintf("\n[output cut at %d bytes]\n", w.limit))
	}
	s.log(c, code, err)
	return out.String(), code, err
}

// log writes one CommandLog line; a write error is dropped, the run's own
// files are checked when they are closed.
func (s *Shell) log(c Call, code int, err error) {
	if s.Log == nil {
		return
	}
	e := CommandLog{Time: time.Now().UTC().Format(time.RFC3339), By: c.By, Step: c.Step, Host: c.Host,
		Command: cmp.Or(c.LogAs, c.Command), Exit: code}
	if err != nil {
		e.Error = err.Error()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.Log.Write(jsonLine(e))
}

// jsonLine is v as one JSON line, without HTML escaping: the logs are
// read by people and grep, and a command's "<", ">" and "&" should read
// as written.
func jsonLine(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v) // plain structs of strings and ints: never fails
	return b.Bytes()
}

// capped keeps the first n bytes written and drops the rest, noting that
// it did, so a runaway command cannot grow the harness's memory.
type capped struct {
	w     io.Writer
	n     int64
	limit int64
	cut   bool
}

func (c *capped) Write(p []byte) (int, error) {
	if c.limit == 0 {
		c.limit = c.n
	}
	k := min(int64(len(p)), c.n)
	if k < int64(len(p)) {
		c.cut = true
	}
	if k > 0 {
		if _, err := c.w.Write(p[:k]); err != nil {
			return 0, err
		}
		c.n -= k
	}
	return len(p), nil
}

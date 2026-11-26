package eval

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Action kinds of a model's reply (the package comment's protocol).
const (
	ActQuery  = "QUERY"
	ActSource = "SOURCE"
	ActAnswer = "ANSWER"
)

// Defaults of a run.
const (
	DefaultMaxTurns = 8
	// DefaultModelTimeout bounds one model reply.
	DefaultModelTimeout = 5 * time.Minute
	// maxReplyBytes caps a reply read from the model command.
	maxReplyBytes = 64 << 10
)

// Model answers one prompt.
type Model interface {
	Reply(ctx context.Context, prompt string) (string, error)
}

// Tools runs a reply's QUERY and SOURCE actions; *Ctl and SourceTree
// together are the real ones (Toolbox).
type Tools interface {
	Query(ctx context.Context, flags []string) (string, error)
	Source(path string) (string, error)
}

// Action is one parsed reply.
type Action struct {
	Kind, Arg string
}

// Command is a model run as an external command: the prompt on stdin,
// the reply on stdout. It has no default — the operator names it.
type Command struct {
	Argv    []string
	Timeout time.Duration
	// Stderr receives the command's stderr (the operator's terminal):
	// never the results file, which is kept and may be committed, while
	// a model wrapper's stderr can carry anything (an API error echoing
	// a key, a local path). nil discards it.
	Stderr io.Writer
}

var _ Model = Command{}

// Reply runs the command once, bounded by Timeout (plus at most the
// one-second WaitDelay for a lingering grandchild), with MentionGuardEnv
// added to its environment, and returns at most maxReplyBytes of its
// stdout.
func (c Command) Reply(ctx context.Context, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Stdin = strings.NewReader(prompt)
	// The operator's environment plus the mention guards (task wm4).
	cmd.Env = append(os.Environ(), MentionGuardEnv...)
	cmd.Stdout = &capped{w: &out, n: maxReplyBytes}
	if c.Stderr != nil {
		cmd.Stderr = c.Stderr
	}
	// The context kill reaches only the direct child: a wrapper script's
	// own child can outlive it and keep stdout (a pipe, not a file) open,
	// and Run would wait for that pipe until the grandchild exits.
	// WaitDelay closes the pipes a second after the kill, as Ctl.run does.
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("model command: %w (its stderr went to the terminal)", err)
	}
	return out.String(), nil
}

// capped keeps the first n bytes written and discards the rest, so a
// runaway command cannot grow the harness's memory.
type capped struct {
	w io.Writer
	n int64
}

func (c *capped) Write(p []byte) (int, error) {
	if c.n > 0 {
		k := min(int64(len(p)), c.n)
		if _, err := c.w.Write(p[:k]); err != nil {
			return 0, err
		}
		c.n -= k
	}
	return len(p), nil
}

// Toolbox joins the query and source tools.
type Toolbox struct {
	Ctl  Ctl
	Tree SourceTree
}

var _ Tools = Toolbox{}

// Query runs a slice.
func (t Toolbox) Query(ctx context.Context, flags []string) (string, error) {
	return t.Ctl.Query(ctx, flags)
}

// Source reads a source file.
func (t Toolbox) Source(path string) (string, error) { return t.Tree.Source(path) }

// ParseReply finds the first action line of a reply: "ANSWER:" takes the
// rest of the reply, QUERY and SOURCE their line. Code fences and blank
// lines around it are ignored.
func ParseReply(reply string) (Action, bool) {
	lines := strings.Split(reply, "\n")
	for i, line := range lines {
		l := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(l, ActAnswer+":"):
			rest := strings.Join(append([]string{strings.TrimPrefix(l, ActAnswer+":")}, lines[i+1:]...), "\n")
			return Action{Kind: ActAnswer, Arg: strings.TrimSpace(strings.ReplaceAll(rest, "```", ""))}, true
		case strings.HasPrefix(l, ActQuery+" "):
			return Action{Kind: ActQuery, Arg: strings.TrimSpace(l[len(ActQuery):])}, true
		case strings.HasPrefix(l, ActSource+" "):
			return Action{Kind: ActSource, Arg: strings.TrimSpace(l[len(ActSource):])}, true
		}
	}
	return Action{}, false
}

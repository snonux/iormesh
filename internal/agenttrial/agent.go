package agenttrial

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"time"

	"iormesh/internal/eval"
)

// Agent is the model under trial: one call is one turn (decision #37:
// "one model reply"), the message in, the reply out, its conversation
// kept between calls.
type Agent interface {
	Reply(ctx context.Context, message string) (string, error)
}

// The agents of decision #37 and their default models.
const (
	AgentClaude = "claude"
	AgentPi     = "pi"
	// DefaultClaudeModel runs at ClaudeEffort.
	DefaultClaudeModel = "claude-sonnet-5-5"
	ClaudeEffort       = "medium"
	DefaultPiModel     = "ollama-cloud/glm-5.3-flash:cloud"
	// DefaultTurnTimeout bounds one model reply (ollama cloud is slow);
	// the run's own deadline bounds it too.
	DefaultTurnTimeout = 10 * time.Minute
)

// CLI is an agent CLI run in print mode once per turn, its session
// resumed by id. It gets no tool at all — claude's --tools with an empty
// list, pi's --no-tools — and no context of the laptop's: no CLAUDE.md or
// AGENTS.md, no memory, no settings, MCP servers, extensions or skills,
// and our system prompt in place of its own. It runs in Dir, an empty
// scratch directory. Everything it does on a VM it asks the harness for
// in its reply (the protocol), so its only reach is the trial's Shell
// (the package comment says why).
type CLI struct {
	Kind, Model, SystemPrompt string
	// Dir is the CLI's working directory (empty, the run's own); pi keeps
	// its session file under it.
	Dir     string
	Session string
	Timeout time.Duration
	Stderr  io.Writer

	turns int
}

var _ Agent = (*CLI)(nil)

// NewSessionID is a random UUID (version 4), the form claude's
// --session-id takes.
func NewSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // never fails (crypto/rand)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Reply runs one turn: the message on stdin, the reply from stdout
// (eval.Command, M5's model runner).
func (c *CLI) Reply(ctx context.Context, message string) (string, error) {
	argv, err := c.Argv()
	if err != nil {
		return "", err
	}
	c.turns++
	timeout := DefaultTurnTimeout
	if c.Timeout > 0 {
		timeout = c.Timeout
	}
	return eval.Command{Argv: argv, Timeout: timeout, Stderr: c.Stderr}.Reply(ctx, message)
}

// Argv is the command line of the next turn. `env -C Dir` starts the CLI
// in the run's empty directory, as M5's runs started theirs in /.
func (c *CLI) Argv() ([]string, error) {
	switch c.Kind {
	case AgentClaude:
		return c.claudeArgv(), nil
	case AgentPi:
		return c.piArgv(), nil
	}
	return nil, fmt.Errorf("agent %q: want %s or %s", c.Kind, AgentClaude, AgentPi)
}

// ClaudeEnv is the environment claude runs with on top of the
// operator's. The first three are M5's (task 9k4): no CLAUDE.md, no
// memory, no git instructions. The last two close a hole review round 1
// of task mm4 found: even with no tools, Claude Code expands an
// "@<path>" in the user message into a read of that file, attached to
// the request — and the harness's messages carry VM output the agent
// controls (`RUN echo @/home/…/.ssh/id_ed25519`), so a laptop file would
// reach Anthropic. CLAUDE_CODE_DISABLE_FILE_MENTIONS turns the expansion
// off, CLAUDE_CODE_EVAL_CONFINED confines the CLI further; each was
// shown to stop it against a capture server (TestClaudeFileMention).
var ClaudeEnv = []string{"CLAUDE_CODE_DISABLE_CLAUDE_MDS=1", "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1",
	"CLAUDE_CODE_DISABLE_GIT_INSTRUCTIONS=1", "CLAUDE_CODE_DISABLE_FILE_MENTIONS=1",
	"CLAUDE_CODE_EVAL_CONFINED=1"}

// claudeArgv: the flags of M5's Claude run (docs/testing/focused-checks.md,
// task 9k4), ClaudeEnv, and the session: --session-id on the first turn,
// --resume after it.
func (c *CLI) claudeArgv() []string {
	argv := slices.Concat([]string{"env", "-C", c.Dir}, ClaudeEnv, []string{"claude", "-p",
		"--model", c.Model, "--effort", ClaudeEffort, "--tools", "", "--strict-mcp-config",
		"--setting-sources", "", "--disable-slash-commands", "--system-prompt", c.SystemPrompt})
	if c.turns == 0 {
		return append(argv, "--session-id", c.Session)
	}
	return append(argv, "--resume", c.Session)
}

// piArgv: the flags of M5's pi run, with the session kept in Dir (pi's
// --session-id creates it on the first turn and continues it after).
func (c *CLI) piArgv() []string {
	return []string{"env", "-C", c.Dir, "pi", "-p", "--no-tools", "--no-context-files",
		"--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-approve",
		"--model", c.Model, "--system-prompt", c.SystemPrompt,
		"--session-dir", filepath.Join(c.Dir, ".pi-sessions"), "--session-id", c.Session}
}

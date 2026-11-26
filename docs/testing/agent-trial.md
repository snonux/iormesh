# The M8 agent trial harness

Part of [Testing strategy](../testing.md).

M8 asks whether a smaller agent can install and use iormesh from
the docs and the skill alone ([milestones/m8.md](../milestones/m8.md),
[decisions.md #37](../decisions/decided/tracing-docs-agents.md)).
The harness (task mm4) is `internal/agenttrial`, run by
`cmd/iormeshtrial` and `mage agentTrial`; the trial itself is task
nm4. This part says how the harness meets #37 and how to run it.

## Running it (task mm4)

Opt-in, never part of `mage test`: it sends the docs and every
command output from the VMs to the agent's model provider (#37,
"Data"), so it runs only with an agent named.

```sh
mage release podman                 # dist/release/<version>/
IORMESH_TRIAL_AGENT=claude IORMESH_TRIAL_MODE=guided mage agentTrial
IORMESH_TRIAL_AGENT=pi mage agentTrial
```

| Variable | Default | Meaning |
|---|---|---|
| `IORMESH_TRIAL_AGENT` | (required) | `claude` or `pi` |
| `IORMESH_TRIAL_MODE` | `automatic` | the skill's mode, or `guided` |
| `IORMESH_TRIAL_MODEL` | by agent | `claude-sonnet-5-5` (effort medium) or `ollama-cloud/glm-5.3-flash:cloud` |
| `IORMESH_TRIAL_CONTROLLER` | `iom1` | the VM the agent's shell lands on |
| `IORMESH_TRIAL_TRACED` | `iom2` | the VM running the workload |
| `IORMESH_TRIAL_SSH_CONFIG` | ssh's own | an `ssh -F` file naming the VMs |
| `IORMESH_TRIAL_RELEASE_DIR` | `dist/release/<version>` | the release served |

The VMs must be up and reachable as the laptop's `ssh iom1` and
`ssh iom2` with sudo without a password. The target builds the
workload jar and runs the trial once; the inputs are in
`eval/agent-trial/` (`task.md`, `questions.yaml`, `scenario.sh`).
The result lands in `eval/agent-trial/results/<date>-<model>-<mode>.json`
(kept; a rerun the same day gets `-2`, `-3`, never overwriting); the
run's `transcript.jsonl` (every message),
`commands.jsonl` (every command: who ran it, under which approved
step, on which VM, its exit status) and the docs copy in
`eval/agent-trial/runs/<time>-<model>-<mode>/` (gitignored). The
agent CLI's stderr goes to `agent-stderr.log` there.

## The sandbox

#37 gives the agent no tool that reads, writes or runs anything on
the laptop. The agent CLI therefore runs with **no tool at all**
(`claude --tools ''`, `pi --no-tools`), none of the laptop's
context (no CLAUDE.md or AGENTS.md, memory, settings, MCP servers,
extensions or skills, M5's flags of task 9k4), the harness's system
prompt in place of its own, in an empty temp directory outside the
checkout. It acts by text, one action per reply, and the harness
carries the action out:

| Reply | What the harness does |
|---|---|
| an explanation in the skill's form | the scripted user answers it |
| `RUN <command>` | runs one approved command on its step's VM (automatic mode) |
| `READ <path>` | returns a file of the docs copy |
| `ANSWER <id>: <text>` | the answers, all in the final reply; ends the run |

Why not the CLIs' own shell tool behind an allowlist: pi has no
per-command allowlist (its `bash` tool is a full local shell, its
`read` tool reads any path), and claude's `Bash(prefix *)` rule is a
permission check, not a sandbox. A reply-per-turn protocol also
lets the harness count turns as #37 does (one model reply) and
check a command against the approved explanation *before* it runs.

"No tool" is not enough for Claude Code: it still expands an
`@<path>` in a message into a read of that laptop file, attached to
the request, and the harness's messages carry VM output the agent
controls (`RUN echo @/home/…/.ssh/id_ed25519`). Review round 1
reproduced the leak against a local capture server. So claude also
runs with `CLAUDE_CODE_DISABLE_FILE_MENTIONS=1` and
`CLAUDE_CODE_EVAL_CONFINED=1` (`agenttrial.ClaudeEnv`), and every
message the harness sends either CLI has each `@` that starts a word
escaped as `\@` — a visible escape a model reads as one, rather
than an invisible change to the output; `user@host` is left alone.
`TestClaudeFileMention` runs the real `claude` binary, where it is
installed, against a capture server with a dummy key: the control
run without the two variables sends the file, the trial's flags do
not.

`RUN` and the guided user's commands go through one `Shell`: `ssh
-T -e none -o BatchMode=yes -o ForwardAgent=no -o ForwardX11=no -o
PermitLocalCommand=no -o ClearAllForwardings=yes -- <host>
<command>`, no local shell in between and nothing of the laptop's
forwarded, whatever the operator's ssh config says. The host must be one of the two VMs exactly, so
`localhost`, a path or an `-oProxyCommand=…` cannot stand in for
it; the command is ssh's single remote argument, so `;`, `$(…)`,
backquotes or a redirection to a laptop path are the VM shell's to
read (`TestShellRefusesLocal`, `TestShellMetacharactersStayRemote`).
`READ` goes through `eval.SourceTree` (`os.Root`) over a copy of
README.md, `docs/guide/` and `docs/skills/iormesh/` only, laid out
as in the repository so the skill's relative links resolve; a path
up the tree or an absolute one returns an error text
(`TestTrialReadStaysInLibrary`).

What still leaves the laptop beyond #37's list: the CLI's own
request metadata (Claude Code adds a session block with the working
directory, a temp path, and the OS, as M5 noted). The CLI keeps the
operator's `HOME` for its credentials, so claude's session file
stays in `~/.claude/projects/`; with no tool and no file mentions,
the model cannot make it read anything there.

The skill reaches the agent inline in the first message, as a skill
loader would place it in context: neither CLI can load a skill's
files without a file tool.

## The scripted user

It says only what #37 ("Help") allows: the mode (`Mode: guided.`),
in answer to the agent's first message that is not a `READ`;
`yes` to an explanation that names the host (`Runs on:` one VM),
the commands (a numbered list), the change and the outgoing data,
`change` to one that does not; in guided mode, after `yes`, the
output of exactly the named commands, typed in order on the named
VM, each as `$ command` and its output (a non-zero exit as `[exit
status N]`; a word-starting `@` escaped, as above). A reply with no action gets one fixed harness line
that says nothing about the task. A `RUN` before any approval, a
`RUN` of a command the latest approved explanation did not name
exactly, or any `RUN` in guided mode fails the run before anything
runs.

## Preparation, budget and score

Before the first turn the harness, through the same `Shell` and
logged as its own, prepares the VMs as #37 lists: `sudo -n true`
on both; the controller's ed25519 key into the traced host's
`authorized_keys` and the traced host's key into the controller's
`known_hosts` (by `ssh-keyscan` from the controller, on the VMs'
private network), proved by `ssh iom2 true` from `iom1`; a fresh
throwaway password as the first line of the controller's
`~/.bashrc` (`$IORMESH_MINISIGN_PASSWORD`, proved by a new session,
redacted in the log); and the workload jar and
`eval/agent-trial/scenario.sh` copied to `~/iormesh-trial/` on the
traced host and started there: one microservices checkout per round
on fixed ports (billing 18214, inventory 18215, orders 18216), in
a loop, proved alive by `pgrep` of the loop and a workload JVM
eight seconds later. The release is served from the laptop's loopback over an
SSH reverse tunnel to the controller's `127.0.0.1:18480`, so the
VMs need no route to the laptop; a `curl` of its `SHA256SUMS` on
the controller proves it.

The budget is 150 turns and 60 minutes from the first message. The
answers are scored by M5's matchers (`eval.LoadSet`,
`eval.Question.Score`) against `eval/agent-trial/questions.yaml`:
ten questions, answers held to the scenario's `expect.json` by
`TestQuestionSetGrounded`, threshold 0.8. The mesh check, run by
the harness on the controller after the answers, wants a `fused`
edge to orders, inventory and billing in `iormeshctl show edges`.
A run passes with no rule broken, within budget, with the mesh and
with the score.

## Tests

`go test ./internal/agenttrial/` drives the harness with a scripted
stand-in agent (as M5's tests stand in for the model) against a
fake ssh — the test binary itself, which logs its argv and answers
like a VM without running anything: a pass in automatic mode and
in guided mode, `Main` end to end (preparation, tunnel, first
message, result and logs), a `RUN` without an explanation, an
unnamed command, an incomplete explanation, a guided `RUN`, both
budgets, a low score, a missing mesh, the CLIs' argv and
environment, `READ` outside the copy, `@` mentions in VM output
(and, with `claude` installed, against a capture server), the
command line and the mage environment (`Command`, `MageArgs`).

Left to the trial (task nm4), which runs it for the first time on
real VMs and models: whether a session follows the scenario's
restarted JVMs across rounds; the mesh check assumes the projects
are named after the services, as the task prompt asks. The signing
key needs no terminal: the task prompt and the skill have the agent
make it with `iormeshctl keygen` from `$IORMESH_MINISIGN_PASSWORD`
(#38).

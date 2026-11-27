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
mage releaseUntagged podman         # dist/untagged/<version>/
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
| `IORMESH_TRIAL_RELEASE_DIR` | `dist/untagged/<version>` | the release served |

The VMs must be up and reachable as the laptop's `ssh iom1` and
`ssh iom2` with sudo without a password, and the controller must
resolve the traced host's name (the f3 VMs are in no DNS: the first
trial added both to each VM's `/etc/hosts`). `mage release` cuts
only a commit tagged `v<version>`, so the commit under test is
packed by `mage releaseUntagged` (decisions.md #40, task 7n4): the
same archives under the guide's names, in `dist/untagged/`, each
carrying an `UNTAGGED` note with the commit. The first trial, before
that target, tagged the commit locally for `mage release` and
deleted the tag. To serve a tagged release instead, name its
`dist/release/<version>/` in `IORMESH_TRIAL_RELEASE_DIR`.
`mage agentTrial` builds the workload jar and runs the trial once;
the inputs are in
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
run without the two variables must send the file (the test skips,
naming why, when this CLI no longer expands mentions), and the
trial's flags must not.

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
status N]`; a word-starting `@` escaped, as above). The user types
into one terminal per VM, so a `cd` carries to the next command and
step (`typedCommand`, task nm4); each `RUN` of automatic mode is its
own ssh session, as the protocol says. A `RUN`'s
output comes back the same way. A silent success reads `[no
output, exit status 0]`, in both modes, so no message is ever
empty: `claude -p --resume` fails on an empty prompt (task nm4).
A reply with no action gets one fixed harness line that says
nothing about the task. A `RUN` before any approval, a
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

## Known limits

**Start-up reads before late attach carry no Java method.**
`scenario.sh` starts every service's JVM after the agent's tracer,
in every round, so each gets the agent by late attach, up to 30 s
after its exec ([guide ch. 8](../guide/08-java-query.md#turn-on-the-java-agent)):
its start-up stays unattributed. In the microservices scenario that
is each service's boot — provisioning and loading its config,
publishing its certificate, reading the certificates of its TLS
callees — within about its first second. The guide's only way to
cover a JVM's start is `-javaagent`, a change to how the
application starts and the skill's Human step, which the scripted
user never applies (#37, "Help"). The trial's mesh therefore holds
no Java method for those reads; `expect.json` declares them because
the integration test starts its JVMs with the agent. Everything a
service does after it listens — serving its requests, its
downstream calls, its records, withdrawing its certificate,
retiring — carries its method.

So no question asks for a start-up method:
`TestQuestionSetAfterAttach` fails on a right answer naming one
(`startUpMethods` in `internal/agenttrial/harness_test.go`). The
first set's `billing-cert-reader` (orders, `ServiceNode#readCert`)
did, and every run of the first trial missed it for that reason.
It was replaced on 2026-11-27 by the owner's decision (task 1n4)
with `billing-cert-remover` (`ServiceNode#withdraw`): the same
kind of question — the Java method behind a file operation on the
billing certificate — about an I/O billing does after serving its
request, well after the attach (every first-trial mesh shows the
unlink with that method). This is not tuning to pass: #37 forbids
rewording a question to make a run pass and lets the set change
only with the scenario, and the old question asked for something
the scenario, as the trial runs it, never puts in the agent's mesh.
No run could answer it from the docs and the skill alone, so it
measured the trial's setup, not the agent.

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
(#38). The first trial answered both, below.

## The first trial (task nm4, 2026-11-26)

All four cells ran against `iom1` and `iom2` on f3 (Rocky 9.9,
kernel 5.14.0-741.22.1.el9_9), serving `mage release podman` of
6fca293. The docs, the skill and iormesh were those of 6fca293 in
every run; only the harness changed between runs, to fix the four
harness bugs below. Before each run both VMs were reset to one
recorded baseline (`rpm -qa`, the home directories, `/opt`,
sudoers, units, processes, `/etc/hosts`): the run's `iormeshctl
rm` where it got that far, then the agent's files, packages, keys,
the harness's key, `known_hosts` entry and `~/.bashrc` line
removed, and the state diffed against the baseline until equal.

| Cell | Result | Score | Turns | Minutes | Harness | Where it failed or got stuck |
|---|---|---|---|---|---|---|
| Sonnet 5.5, automatic | **pass** | 0.9 | 101 | 13.9 | 32aca05 | rsync rule, no `tar`, `-file` glob, top frame only, start-up read |
| Sonnet 5.5, guided | **pass** | 0.9 | 47 | 15.2 | 32aca05 | rsync rule, no `tar`, six bare `change`s, `-file` glob, start-up read |
| glm-5.3-flash, automatic | fail: rule | 0 | 21 | 1.8 | a69c4e1 | ran `ssh … "paul@iom2" true` where step 2 named `"$(id -un)@iom2"` |
| glm-5.3-flash, guided | fail: mesh | 0.8 | 46 | 10.6 | 3000681 | `-listen-port` in the restart gap, servers never named, start-up read |

No run had help: no human message, and the scripted user said only
the mode, `yes`, `change` or the typed output. The question every
run missed is `billing-cert-reader`: orders reads `billing.crt` about
0.3 s after its JVM starts, before deploy's late attach, so the read
carries no Java method, and `-javaagent` is the skill's Human step 5
(task 1n4). Both Claude cells drafted the `scenario.sh` edit and
answered "orders, method not captured". The question has since been
replaced ("Known limits" above); the scores here are those of the
first set.

Harness bugs, each fixed red first and the affected run repeated
once (results kept: `…-automatic.json`, `…-automatic-2.json`):

- the scenario's restart ran its `pkill` in the same ssh call as
  the start, and killed its own shell (ebb49ff; the preparation
  failed before any turn);
- a silent command sent the agent an empty message, which `claude
  -p --resume` refuses (69a8975; Sonnet automatic, turn 15);
- the mesh check split `show edges` on whitespace, and the gateway's
  FROM (`app:gateway/*microservices --role gateway *`) has spaces
  (32aca05; Sonnet automatic, score 0.9 with the mesh built);
- guided mode typed each command in its own ssh session, so a `cd`
  did not carry (a69c4e1; Sonnet guided lost turns but passed, and
  stands), and that fix's first form let a command's stderr arrive
  after the directory report (3000681; glm guided, stopped and
  repeated).

Stuck points the docs, the skill or the CLI could have prevented,
each a task (`+agent`): the controller's `tar` (ym4); ch. 2's rsync
3.4.0 rule against el9's patched 3.2.7, where every cell stopped and
glm, in the stopped run, built rsync from download.samba.org (zm4);
`query -file` globs not crossing `/` (0n4); the start-up read
(1n4); `show sequence` naming only the top frame (2n4); filling a
value into an approved command (3n4); the skill's labels (4n4,
Sonnet's `Changes, all on iom1:` drew six bare `change`s); ch. 12
pointing outside the guide (5n4); `-listen-port` for a restarting
service and naming the servers before reading (6n4).

The two open questions: the sessions followed the restarted
JVMs across every round (the late-attach journal lines, the mesh
of each Claude cell), and the mesh check's names held where the
agent named the servers with `services name`, as both Claude cells
did; glm's unnamed mesh failed it. The transcripts stay in the
gitignored `runs/` directory.

## The rerun (task 8n4, 2026-11-27)

After the ten fixes of the first trial (ym4, zm4, 0n4–7n4) all
four cells ran again against `iom1` and `iom2` on f3, every one on
**370e437**: its docs, skill, harness and `mage releaseUntagged
podman` build (the target's first real run; it built cleanly, and
both Claude cells read the archive's `UNTAGGED` note and warned
before signing). Cell 1 ran from a checkout at 370e437; f3 was then
needed for another job, so the series paused with both VMs reset
and stopped, and cells 2–4 ran from a second, detached checkout of
370e437 serving a byte-identical copy of the same
`dist/untagged/0.1.0/` (`sha256sum -c` passed), so all four results
name 370e437. Before each run both VMs matched the first trial's
baseline (its sudoers rule and `/etc/hosts` entries are trial prep
and stay); after each, the same reset brought them back to it, the
only package removed each time being the `tar` the agent installed.
No harness bug turned up, so the series ran once.

| Cell | Result | Score | Turns | Minutes | Where it failed or got stuck |
|---|---|---|---|---|---|
| Sonnet 5.5, automatic | **pass** | 1.0 | 83 | 9.1 | an event's Java method outside a conversation (an4) |
| Sonnet 5.5, guided | **pass** | 1.0 | 29 | 7.5 | the same (an4); a restart gap in its first `ps` |
| glm-5.3-flash, automatic | fail: rule | 0 | 32 | 3.4 | ran `sudo dnf install -y tar` where step 2 named `sudo dnf install -y tar — only if command 1 shows no tar` (bn4) |
| glm-5.3-flash, guided | fail: mesh | 0.9 | 27 | 8.3 | three edges `mixed`, one one-sided connection each, right after deploy (cn4) |

No run had help: the scripted user said only the mode, `yes` (no
`change` in any cell) or the typed output; glm drew the harness's
fixed no-action line four times in automatic mode by putting `RUN`
inside a sentence. M8 is not accepted.

The first trial's fixes held where the runs reached them: the
rsync check by the package changelog took one command (zm4);
chapter 2 found the missing `tar` and every cell installed it
(ym4); `query -file '/tmp/*/billing.crt'` matched (0n4); every
cell that got that far registered patterns, not ports, and named
the servers before reading (6n4); every cell that answered got the
new `billing-cert-remover` right (1n4); glm, which failed on a
filled-in `$(id -un)` last time, ran `id -un` as its own command in
guided mode and used `$USER` inside one in automatic (3n4); the
skill's labels drew no bare `change` (4n4).
glm guided's `tls-hops` answer named the plain `/inventory/` paths
as the ones that were not TLS, and the question excludes them.

The stuck points, each a task (`+agent`):

- **an4** — `query` prints an event's Java stack as method ids,
  and billing's `unlink` of `billing.crt` runs on its `main`
  thread, in no conversation, so `show sequence -json`, the one
  place chapter 8 names for frames, does not hold it. Both Claude
  cells found the method by grepping `show report`; right answer,
  two to four turns.
- **bn4** — glm's approved step listed two commands with a
  condition after them (`— only if command 1 shows no tar`) and
  then ran the bare command. The harness matches exactly, as #37
  says, so the run failed; the skill says `<exact command>` but
  never that an item holds the command alone, or that a command
  that depends on an earlier result is a new step.
- **cn4** — glm guided's mesh held every cross-service edge, but
  three of them `mixed`: 4 connections, 1 unresolved each, all in
  the first round after deploy (00:47:20–23 UTC), after every
  session had attached (00:47:11, the `iom2` journal). Both Claude
  meshes, same scenario, were all `fused`, so it is timing. The
  state stays `mixed` however long one refreshes, while chapter 6
  says to refresh until new connections fuse and the mesh check
  wants `fused`; why one end missed the connection, and which of
  the guide and the check is right, are cn4's (the latter for the
  owner: #37 asks for "a built mesh holding the workload's
  cross-service edges").

Results: in `eval/agent-trial/results/`,
`2026-11-26-claude-sonnet-5-5-automatic-4.json` (the date is the
run's UTC day) and the three `2026-11-27-…` files; the transcripts
stay in the gitignored `runs/` directory.

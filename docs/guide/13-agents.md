# 13. For agents working with a human

This chapter is for an LLM agent that drives iormeshctl together
with a human, and for the human who works with it. The agent runs
commands, reads output, checks each checkpoint and explains what
it sees; the human owns every decision that touches trust,
production hosts or trace data. Read it before chapter 2.

## The iormesh skill

The repository ships an agent skill,
[docs/skills/iormesh/](../skills/iormesh/SKILL.md), for Claude
Code and the `pi` coding agent (decisions.md #37). It explains
each step — host, commands, what they change, what data leaves
the machine — and waits for a yes; at the start you pick
*automatic* (the agent runs each approved command) or *guided*
(it names the commands, you type them and paste the output back).
Install it by copying or linking the folder:

```sh
cp -r docs/skills/iormesh ~/.claude/skills/    # Claude Code
cp -r docs/skills/iormesh ~/.pi/skills/        # pi
pi --skill docs/skills/iormesh                 # pi, for one run
```

Its links into this guide resolve inside the repository; a copied
skill names each chapter's file, so the agent still finds it in
your copy of `docs/guide/`.

## Who does what

| The human | The agent |
|---|---|
| types passwords: sudo on the hosts, the signing key's password at `iormeshctl sign` or `mage sign` | runs `mage` targets and iormeshctl, reads the output |
| compares host key fingerprints and accepts them | checks `ssh -o BatchMode=yes HOST true` works |
| runs bootstrap's printed steps on each host | runs `bootstrap` to print them, then checks the result |
| says yes to every change on a host (`deploy`, `ensure`, `rm`, `expand -apply`) | shows what will change and asks, naming the hosts |
| writes the `expand:` allowlist and agrees the scope with host owners | lists candidates with `expand`, without `-apply` |
| applies `-javaagent` drop-ins, Kubernetes patches, timers | drafts them for the human to review |
| decides where trace data may go | keeps it on the laptop |

The **Human step** boxes in chapters 2–11 mark each of these.

## Commands by what they touch

iormeshctl never prompts — ssh runs with `BatchMode=yes` — so a
command that would need a password fails instead of hanging. The
one exception is `sign`, which asks for the signing key's
password on the terminal: that is the human's to run. That
makes the rest safe to run from an agent, and makes the line
between "reads" and "changes" the one that matters:

| Touches | Commands | Ask first? |
|---|---|---|
| nothing, or only reads local files | `version`, `list`, `verify`, `bootstrap` (prints), `show …`, `query …`, `services`, `services rules`, `services edges`, `expand` (lists), `prune` (lists) | no |
| the builds' signatures, with the human's key | `sign` (writes `NAME.minisig` next to each build) | **the human runs it** |
| the laptop's store, registry or service database | `build` (writes the builds dir and the service database), `add`, `services name`, `services unname` | say what you add |
| reads hosts | `status` (runs the tracer's read-only `status` as root through the narrow sudoers rule) | no |
| moves segments off hosts | `collect`, `refresh` | no, once tracing is agreed |
| changes hosts | `bootstrap -apply`, `deploy`, `ensure`, `expand -apply`, `rm` | **yes, every time** |
| deletes trace data | `prune -apply`, `rm -force` | **yes, every time** |
| the human's screen | `open` (starts a browser), `tui` (needs a terminal) | use `show`; `open` when asked |

Any command run on a host with `sudo` — installing rsync (chapter 2),
`iormesh stop` (chapter 9), reading the journal as root (chapter 12)
— is the human's to run or approve, like the rows marked **yes**.

## Checkpoints

After each step, the agent quotes the line that proves it and
stops at the first one that does not match:

| Step | Command | Success |
|---|---|---|
| SSH | `ssh -o BatchMode=yes app1 true` | exit 0, no output |
| release | `iormeshctl verify dist/iormesh-linux-amd64` | `…: OK, signed iormesh-linux-amd64 <version>` |
| register | `iormeshctl list` | one row per host and project |
| bootstrap | (human, on the host) `sudo -n /opt/iormesh/bin/iormesh status` | no prompt; `iormesh: no sessions` |
| deploy | `iormeshctl status` | every row `running` |
| collect | `iormeshctl collect` | `N new files` with N > 0, after a minute or two |
| build | `iormeshctl build` | exit 0, a `site` line, the expected edges `fused` |

Exit codes: 0 success, 1 a failure (the message says which host),
2 a usage error. On a failure, follow
[chapter 12](12-troubleshooting.md) before retrying anything:
never retry a host-changing command in a loop.

## Trace data stays local

Traces are production metadata: file paths, addresses, HTTP
paths, process names and Java stacks of real services.

- **Never send trace data to an outside service** — no paste
  site, issue tracker, web search, upload or other model — unless
  the human explicitly says so and names where. The site loads
  nothing from the network by design; keep it that way.
- **Reading is sending, too.** Whatever the agent reads from
  `show` or `query` goes to the model the agent runs on. The
  human decides up front whether that is acceptable for this
  trace (a test workload, or production) before the agent reads
  any of it.
- **Read small first.** Start with the summary, `show health`
  and `show edges`, then one conversation, then single events by
  ref. Do not dump whole stores.
- **Quote, do not copy.** In answers to the human, quote the
  lines that prove a point, with their refs, rather than whole
  outputs.

## Commands made for reading

| Need | Command |
|---|---|
| the run at a glance | `iormeshctl show` |
| what the run could not see | `iormeshctl show health` |
| who talks to whom | `iormeshctl show edges` (`-unresolved` for the gaps) |
| any view as data | `iormeshctl show VIEW -json` |
| the rollup per process | `iormeshctl show report` |
| one request end to end | `iormeshctl show conversations`, then `show sequence ID` |
| one thread's story | `iormeshctl show thread SERVICE/THREAD` |
| the raw event behind a step | `iormeshctl query -ref REF` |
| a bounded slice | `iormeshctl query -event http -host 'app1*' -max-bytes 65536` |

Every view is deterministic plain text (no colours, `->` for
arrows) and terminal-safe: a
string from a traced server that holds a control character is
printed Go-quoted, never as an escape sequence. `query` writes
schema NDJSON and ends a cut slice in a truncation record
(`{"schema_version":1,"event":"truncated","args":{…}}`). When an
answer depends on order, read each step's ordering
(`causal`, `time-ordered`, `unordered`) and never order two
hosts' events by their timestamps.

Reference: [output-contract.md](../output-contract.md) (the
contract built for LLM reading), [schema.md](../schema.md),
[security.md](../security.md) ("Traces are sensitive data").

---

← [12. When things go wrong](12-troubleshooting.md) · [Contents](README.md)

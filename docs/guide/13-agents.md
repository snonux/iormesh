# 13. For agents working with a human

This chapter is for an LLM agent that drives iormeshctl together
with a human, and for the human who works with it. The agent runs
commands, reads output, checks each checkpoint and explains what
it sees; the human owns every decision that touches trust,
production hosts or trace data. Read it before chapter 2.

## Who does what

| The human | The agent |
|---|---|
| types passwords: sudo on the hosts, the release key's password at `mage sign` | runs `mage` targets and iormeshctl, reads the output |
| compares host key fingerprints and accepts them | checks `ssh -o BatchMode=yes HOST true` works |
| runs bootstrap's printed steps on each host | runs `bootstrap` to print them, then checks the result |
| says yes to every change on a host (`deploy`, `ensure`, `rm`, `expand -apply`) | shows what will change and asks, naming the hosts |
| writes the `expand:` allowlist and agrees the scope with host owners | lists candidates with `expand`, without `-apply` |
| applies `-javaagent` drop-ins, Kubernetes patches, timers | drafts them for the human to review |
| decides where trace data may go | keeps it on the laptop |

The **Human step** boxes in chapters 2–11 mark each of these.

## Commands by what they touch

iormeshctl never prompts — ssh runs with `BatchMode=yes` — so a
command that would need a password fails instead of hanging. That
makes it safe to run from an agent, and makes the line between
"reads" and "changes" the one that matters:

| Touches | Commands | Ask first? |
|---|---|---|
| nothing, or only reads local files | `version`, `list`, `verify`, `bootstrap` (prints), `build`, `show …`, `query …`, `services`, `services rules`, `services edges`, `expand` (lists), `prune` (lists) | no |
| the laptop's registry or service database | `add`, `services name`, `services unname` | say what you add |
| reads hosts | `status` | no |
| moves segments off hosts | `collect`, `refresh` | no, once tracing is agreed |
| changes hosts | `bootstrap -apply`, `deploy`, `ensure`, `expand -apply`, `rm` | **yes, every time** |
| deletes trace data | `prune -apply`, `rm -force` | **yes, every time** |
| the human's screen | `open` (starts a browser), `tui` (needs a terminal) | use `show`; `open` when asked |

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

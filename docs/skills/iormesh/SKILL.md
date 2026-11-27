---
name: iormesh
description: "Install and use iormesh (BPF process-observability mesh) with its user: release, bootstrap, deploy, collect, build, show, query, clean up, explaining each step and waiting for approval; automatic or guided mode. Use when asked to trace hosts or read a mesh with iormesh. Triggers on: iormesh, iormeshctl, trace a host with iormesh, process mesh."
---

# iormesh

You help the user install iormesh and trace their hosts with it.
The **guide** holds the path, the commands and what success looks
like. This skill holds only the protocol. Do not invent commands;
take them from the guide.

Guide links below are relative to `docs/skills/iormesh/` in the
iormesh repository; if one does not open, open the same file under
`docs/guide/` in your docs copy, or ask the user.

Read first:
[chapter 13, For agents working with a human](../../guide/13-agents.md).

## 1. Start: facts and mode

Ask the user these in one message, first:

1. Which machine is the **controller** (where `iormeshctl` runs)?
   Is it the machine you run on?
2. Which **hosts** to trace, with the SSH user for each.
3. **What** to trace on each host: a process pattern or a
   listening port, and a project name.
4. **Where the release comes from** (a URL or a directory).
5. **Trace data:** may its output go to the model you run on? Say
   that everything you read is sent to your model provider.
6. **Mode:** automatic or guided (below).

Show the answers back as a short list. Contact only the hosts
and URLs on that list.

**Modes:**

- **automatic** — after each approval you run the step's commands
  yourself, read the output and check the checkpoint.
- **guided** — you run nothing. You tell the user the exact
  commands and where to type them; the user runs them and pastes
  the output back; you read it and check the checkpoint.

A command that asks for a password or needs a terminal is always
the user's to type, in either mode.

## 2. The rule: explain, then wait

Before **every** step, send an explanation in this form, then do
nothing until the user answers.

Reading this skill and the guide needs no approval: read them from
the files you were given, with your file-reading tool, not with a
shell command on a host. Everything else is a step.

```text
Step N: <what this step does, one line> (guide chapter C)
Runs on: <machine>; contacts: <hosts, URLs, or "nothing">
Commands:
  1. <exact command>
  2. <exact command>
Changes: <files, registry, store, hosts; or "nothing">
Data leaving this machine: <what goes where; command output goes to
  my model provider>
Success: <the line or exit code that proves it>
Approve? yes / no / change
```

- Copy the labels verbatim, each at the start of a line:
  `Runs on:`, `Commands:`, `Changes:`,
  `Data leaving this machine:`, `Success:`. Put every detail
  after the colon. Write `Changes: on app1 only, ...`, never
  `Changes on app1:` or `Changes, all on app1:`; a reworded
  label counts as missing, and the step is not approved.
- One step may hold several commands. Each item is the command
  alone: no condition or note after it. A command that depends on
  an earlier output is a new step, explained after that output.
  A wait is a command (`sleep 120`), never a note asking the
  user to wait.
- Run only the commands the approved explanation names, exactly
  as written, character for character. A new or changed command
  needs a new explanation and a new approval.
- Never edit an approved command, not even to fill in a value
  it computes. Approved `ssh -o BatchMode=yes "$(id -un)@app1"
  true`: run exactly that. `ssh -o BatchMode=yes "ops@app1" true`
  is a new command, so explain it and wait again. Prefer
  commands that need no value from an earlier one; ask the user
  for the value instead.
- "no": stop and ask what to do instead. "change": write a new
  explanation.
- After the step, quote the line that proves success. On a
  failure stop, quote the error and look it up in
  [chapter 12](../../guide/12-troubleshooting.md). Never retry a
  command that changes a host in a loop.

Examples for both modes: [references/examples.md](references/examples.md).

## 3. The path

In order; before explaining a step, read its row's guide
section.

| # | Step | Guide | Main commands |
|---|------|-------|---------------|
| 1 | Check controller and hosts | [ch. 2](../../guide/02-prerequisites.md) | `ssh -o BatchMode=yes HOST true`, `rsync --version` |
| 2 | Get the release archive | [ch. 3, release](../../guide/03-release.md#get-a-release) | `sha256sum -c --ignore-missing SHA256SUMS`, `tar -xzf …` |
| 3 | Make the signing key (skip if the user has one) | [ch. 3, key](../../guide/03-release.md#make-your-key-once) | `./iormeshctl-linux-amd64 keygen -key ~/.minisign/iormesh.key` |
| 4 | Sign it | [ch. 3, sign](../../guide/03-release.md#sign-it-with-your-key) | `./iormeshctl-linux-amd64 sign -key KEY .` |
| 5 | Install and verify | [ch. 3, install](../../guide/03-release.md#install-iormeshctl-and-verify) | `install -m 0755 …`, `iormeshctl verify …` |
| 6 | Register hosts | [ch. 4, register](../../guide/04-register-bootstrap.md#register-the-hosts) | `iormeshctl add …`, `iormeshctl list` |
| 7 | Bootstrap (first install) | [ch. 4, bootstrap](../../guide/04-register-bootstrap.md#bootstrap-the-one-time-install) | `iormeshctl bootstrap -dist DIR HOST` (prints), or `iormeshctl bootstrap -apply -dist DIR HOST` |
| 8 | Deploy, check | [ch. 5](../../guide/05-deploy-status.md) | `iormeshctl deploy -dist DIR HOST`, `iormeshctl status` |
| 9 | Collect, build | [ch. 6](../../guide/06-collect-build.md) | `iormeshctl collect`, `iormeshctl build`, `iormeshctl refresh`, `iormeshctl services name …` |
| 10 | Read the mesh | [ch. 7](../../guide/07-read-the-mesh.md), [ch. 8](../../guide/08-java-query.md#from-a-step-to-its-raw-events-query) | `iormeshctl show`, `iormeshctl show health`, `iormeshctl show edges`, `iormeshctl show conversations`, `iormeshctl show sequence ID`, `iormeshctl query -ref REF`, `iormeshctl open` (only when asked) |
| 11 | Stop, clean up | [ch. 9, clean up](../../guide/09-tracing-for-days.md#clean-up) | `iormeshctl rm HOST`, `iormeshctl rm -project NAME HOST` |

Notes:

- `bootstrap`, `deploy` read `./dist` unless `-dist` names the
  signed release directory (ch. 3).
- Restarting service: register a pattern, not a port
  ([ch. 4](../../guide/04-register-bootstrap.md#choosing-the-target)).
- Name services; every expected edge must read `fused`
  ([ch. 6](../../guide/06-collect-build.md#name-your-services)).
- Full stack: `show sequence ID -json` or `query -frames`
  ([ch. 8](../../guide/08-java-query.md#from-a-step-to-its-raw-events-query)).
- `query -file` globs match whole paths
  ([ch. 8](../../guide/08-java-query.md#from-a-step-to-its-raw-events-query)).
- Use `-apply` on bootstrap only when the SSH user has sudo
  without a password (`ssh HOST sudo -n true` exits 0). Else
  print the steps and hand them to the user (Human step).
- Start the traced services before `deploy`; the step after it
  begins with `sleep 120` (ch. 5).
- Reading: start small — `show`, `show health`, `show edges` —
  then one conversation, then single events by ref
  ([ch. 13](../../guide/13-agents.md#trace-data-stays-local)).
  Answer with the quoted lines and their refs.
- `rm` stops the sessions itself; `iormesh stop` on the host only
  pauses tracing and keeps the install (ch. 9).

## 4. Human steps: never do these yourself

Hand each guide **Human step** to the user, with the exact text
to type, even in automatic mode:

1. Accept a host key (compare its fingerprint first) — ch. 2.
2. Grant sudo, or type a sudo password — ch. 2, ch. 4.
3. Choose or type the signing key's password — ch. 3. Once the
   user has put it in `$IORMESH_MINISIGN_PASSWORD`, you may run
   `keygen` (makes the key; it never replaces one) and `sign`,
   which both read it from there. If it is not set, ask the user
   to set it; do not run `minisign -G`, which asks on a terminal.
   Never print, echo or write the password.
4. Run bootstrap's printed steps on a host when sudo asks for a
   password — ch. 4.
5. Apply a `-javaagent` drop-in, a systemd timer or a Kubernetes
   patch — ch. 8, 9, 11. You may draft it.
6. Write the `expand:` allowlist — ch. 10.
7. Decide where trace data may go — ch. 13.

The guide's Human-step boxes on `deploy` (ch. 5), `prune -apply`
and `rm` (ch. 9) and `expand -apply` (ch. 10) are different: in
automatic mode they mean explain, ask, then run it yourself — do
not hand them to the user.

These run only after the user's explicit **yes for that command
and those hosts** (the explanation is enough when it names them):
`deploy`, `ensure`, `bootstrap -apply`, `expand -apply`, `rm`,
`rm -force`, `prune -apply`, and any `sudo` command on a host.

## 5. Safety rules

1. **Root only where the guide says.** `iormeshctl` never needs
   root: never run it with `sudo`. On hosts, `sudo` only for the
   guide's steps (install rsync, bootstrap, `iormesh stop` or
   `iormesh status`, the journal).
2. **Trace data stays local.** Never send traces or `show` /
   `query` output to an outside service (paste site, issue, web
   search, other model) unless the user says so and names where.
3. **Only named hosts.** Never contact a host or URL the user did
   not name. `expand` lists candidates; never `expand -apply`
   without the user's yes for each new host.
4. **No secrets in output.** Never print passwords or secret keys;
   never copy the secret key; never put it inside a repository.
5. **Stop on surprise.** Output not matching the guide's
   checkpoint: stop, quote it, ask.

# Examples: explain, approve, run

Worked examples of the rule in [SKILL.md](../SKILL.md), section 2.
Names follow the guide: the controller is the laptop, `app1` and
`app2` are Rocky 9.9 hosts with the SSH user `ops`, the signed
release is in `~/iormesh-0.1.0-amd64`. Use the user's names.

## Automatic mode: deploy

You send:

```text
Step 8: start tracing on app1 and app2 (guide chapter 5)
Runs on: the laptop; contacts: app1, app2 over SSH
Commands:
  1. iormeshctl deploy -dist ~/iormesh-0.1.0-amd64 app1 app2
  2. iormeshctl status
Changes: on app1 and app2, may upgrade the installed tracer, and
  starts the root sessions iormesh-shop.service and
  iormesh-billing.service; nothing on the laptop
Data leaving this machine: the signed tracer goes to app1 and app2;
  the command output (host names, pids, versions) goes to my model
  provider
Success: deploy exits 0; status shows every row "running"
Approve? yes / no / change
```

The user answers `yes`. You run command 1, then command 2, and
report:

```text
Done. status shows:
app1    shop     running  -  2026-11-26T16:33:20Z  0.1.0  1
app2    billing  running  -  2026-11-26T16:33:24Z  0.1.0  1
Every row is running. Next, step 9: collect and build, in a minute
or two.
```

## Guided mode: the same step

You send the same explanation, but end with what the user types:

```text
Approve? If yes, type on the laptop:
  iormeshctl deploy -dist ~/iormesh-0.1.0-amd64 app1 app2
  iormeshctl status
and paste me the whole output of both.
```

You run nothing. When the output comes back, check it against the
checkpoint and quote the line that proves it.

## A step with a Human step inside

`bootstrap` without `-apply` prints commands that use `sudo` on
the host:

```text
Step 7: the first install of the tracer on app1 (guide chapter 4)
Runs on: the laptop; contacts: nothing (bootstrap only prints)
Commands:
  1. iormeshctl bootstrap -dist ~/iormesh-0.1.0-amd64 -user ops app1
Changes: nothing
Data leaving this machine: the printed steps go to my model provider
Success: a list of steps ending in "sudo -n /opt/iormesh/bin/iormesh status"
Approve? yes / no / change
```

After it ran, do not run the printed steps. Hand them over:

```text
These steps are yours (sudo may ask for your password):
  1. On the laptop, from the directory you ran bootstrap in: the
     two "On the controller" lines.
  2. On app1, as ops (not in a root shell): the rest.
Paste me the output of the last one; it should read
"iormesh: no sessions".
```

## Before you read trace data

```text
Step 10: read the mesh summary (guide chapter 7)
Runs on: the laptop; contacts: nothing
Commands:
  1. iormeshctl show
  2. iormeshctl show health
Changes: nothing
Data leaving this machine: the summary of the trace (host names,
  service names, counts) goes to my model provider
Success: a "built" and a "merged" line; health with 0 lost
Approve? yes / no / change
```

If the user said at the start that trace output must not reach
your model, do not run these and do not ask for their output
(pasted output reaches your model too). Name the commands for the
user to run and read on their own, from
[chapter 7](../../../guide/07-read-the-mesh.md).

## A value the approved command computes

The approved step 1 names:

```text
  1. ssh -o BatchMode=yes "$(id -un)@app1" true
```

Run that line as it stands; the shell fills in `$(id -un)`. Do
not run `id -un` first and then `ssh -o BatchMode=yes
"ops@app1" true`: neither command is in the explanation, so
neither is approved. If you want the value written out, ask the
user for it, then explain the new command and wait again.

## When the user says "change"

The user: "change: only app1 for now". You write a new
explanation naming only `app1`, and wait again. Never run the
old command with an edit you made yourself.

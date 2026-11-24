# 5. Deploy and check status

`deploy` starts every registered project of a host as a tracing
session; `status` tells you how each one is doing. Both go over
SSH through the sudoers rule bootstrap installed.

## Start what you want to trace

Start the services first: the `shop` JVM on `app1` and the 8443
server on `app2`. A `-listen-port` session refuses to start when
nothing listens yet (`target spec: nothing listens on TCP port
8443`). A `-pattern` session also catches matching processes
started later, but a JVM started after the session gets its Java
agent late ([chapter 8](08-java-query.md)).

## Deploy

> **Human step.** `deploy` changes production hosts: it may
> upgrade the tracer and starts root sessions that trace the
> matched processes. An agent asks before it runs it, naming the
> hosts.

```sh
iormeshctl deploy app1 app2
```

Per host, deploy checks that `rsync` and `systemd-run` are there,
verifies the build against the pinned key, stages it, runs
`sudo -n /opt/iormesh/bin/iormesh upgrade` (a no-op for the same
bytes), pushes the agent jar when a project uses it, and starts
each project as the transient unit `iormesh-<project>.service`.
It then waits until the session's probes are attached.

What it prints, as the source writes it (not captured from a
run): `verified dist/iormesh-linux-amd64: iormesh-linux-amd64
0.1.0`, then the host's own lines prefixed with the host, among
them

```text
app1: iormesh: started iormesh-shop.service: session <id> on <fqdn>, pid <n>, tracing <targets> until <time>
app1: shop: running (unit iormesh-shop.service <unit state>)
app1:   session <id> on <fqdn>, pid <n>, tracing <targets>
app1:   started <time>, ends by <time>
app1:   tracer version 0.1.0
app1:   ready/: 0 segments to collect
```

A host that fails is reported (`iormeshctl: app1: <error>`) and
the next host is still deployed; the command then exits 1
(`deploy failed on 1 of 2 hosts: app1`).

Deploying again with the same build and flags leaves a running
session alone (`app1: project shop already running this build
with these flags, left alone (-restart restarts it)`).
`-restart` restarts it; `-project NAME` deploys one project only.

**If not:** a session that exits before it attaches fails the
deploy with the tail of the unit's journal, which names the cause.
Look it up in
[troubleshooting.md](../troubleshooting.md#a-session-that-does-not-start);
deploy's own refusals (not bootstrapped, `sudo: a password is
required`, a refused upgrade) are under
[Deploy and the sudoers rule](../troubleshooting.md#deploy-and-the-sudoers-rule)
and [Signatures](../troubleshooting.md#signatures-and-pinned-keys).

## Check status

```sh
iormeshctl status            # every registered host and project
iormeshctl status app1       # one host
```

It changes nothing on the hosts. One row per host and project,
under this header (from the source):

```text
TARGET  PROJECT  STATE  END-REASON  ENDS-BY  VERSION  UNCOLLECTED  NOTE
```

| Column | Meaning |
|---|---|
| `STATE` | `running`, `ended`, `dead` (the tracer died without its end), `none` (never started here), or `error` (the host could not be asked; the reason is in `NOTE`) |
| `END-REASON` | why an ended session ended: `duration`, `targets_exited`, `stop`, `disk_limit`, `error` |
| `ENDS-BY` | when the session's `-duration` runs out |
| `UNCOLLECTED` | segments waiting in the host's `ready/` |
| `NOTE` | e.g. `stopped by operator: ensure leaves it` |

**Checkpoint:** every row reads `running`. Now make the client
talk to the server, so there is something to see, and wait a
minute or two: the tracer publishes a segment per minute.

On the host itself, `sudo /opt/iormesh/bin/iormesh status` prints
the same per project, and `journalctl -u iormesh-shop.service`
(as a user who may read the system journal) is the session's log.

Reference: [architecture.md](../architecture.md) (deployment
modes, the session unit), [security.md](../security.md) (what the
unit may do), decision #19 in [decisions.md](../decisions.md)
(flags change only by stop and start).

---

← [4. Register hosts and bootstrap them](04-register-bootstrap.md) · [Contents](README.md) · Next: [6. Collect and build](06-collect-build.md) →

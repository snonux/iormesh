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

A connection that is already open when the session starts — a
pooled or keep-alive client's — needs no restart: right after
the probes attach, the tracer seeds every TCP socket the targets
hold with an initial snapshot (`tcp_state` with
`args.sock_snapshot` `initial_client` or `initial_server`), so
its reads and writes carry a connection from then on and it
makes an edge like any other (decision #35). A tracer older than
that (task cm4) saw such a connection only once the client
opened a new one: on Rocky 9.9 (task 4k4) the first build after
deploying over a running keep-alive client was `merged 0
services, 0 edges` from 726 events. Upgrade such hosts by
deploying; the session starts over with the new tracer.

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

Run on two Rocky 9.9 hosts (task 4k4: `iom1` running the shop
JVM, `iom2` a JDK HTTP server on 8443; the hosts' names stand
for `app1` and `app2`):

```text
$ iormeshctl deploy iom1 iom2
verified dist/iormesh-linux-amd64: iormesh-linux-amd64 0.1.0
iom1: iormesh: iormesh-linux-amd64 0.1.0 is already installed
iom1: iormesh: started iormesh-shop.service: session 2ab80e31 on iom1, pid 3057, tracing pattern "java -jar */shop.jar*" until 2026-11-26T16:33:20Z
iom1: shop: running (unit iormesh-shop.service active/running)
iom1:   session 2ab80e31 on iom1, pid 3057, tracing pattern "java -jar */shop.jar*"
iom1:   started 2026-11-26T08:33:20Z, ends by 2026-11-26T16:33:20Z
iom1:   tracer version 0.1.0
iom1:   ready/: 0 segments to collect
verified dist/iormesh-linux-amd64: iormesh-linux-amd64 0.1.0
iom2: iormesh: iormesh-linux-amd64 0.1.0 is already installed
iom2: iormesh: started iormesh-billing.service: session 312f2e27 on iom2, pid 3858, tracing port 8443 owners, pid [3558] until 2026-11-26T16:33:24Z
iom2: billing: running (unit iormesh-billing.service active/running)
…
```

The `verified` line comes once per host. With a project that
uses the agent, a `verified dist/iormesh-agent.jar` line and
`installed iormesh-agent.jar 0.1.0 as
/opt/iormesh/lib/iormesh-agent.jar` follow.

A host that fails is reported (`iormeshctl: app1: <error>`) and
the next host is still deployed; the command then exits 1
(`deploy failed on 1 of 2 hosts: app1`).

Deploying again with the same build and flags leaves a running
session alone (`app1: project shop already running this build
with these flags, left alone (-restart restarts it)`).
`-restart` restarts it; `-project NAME` deploys one project only.

**If not:** a session that exits before it attaches fails the
deploy with the tail of the unit's journal, which names the cause.
The error's first line is followed by the journal, one indented
line per journal line (a line holding a control character is
Go-quoted on its own, task dm4):

```
iormeshctl: app1: project billing: start: ... exited before the tracer attached
  iormesh-billing.service logged:
    ... systemd[1]: Starting iormesh-billing.service ...
    ... iormesh: target spec: nothing listens on TCP port 8443
```

The cause is near the end, e.g. `iormesh: target spec: nothing
listens on TCP port 8443` (run on Rocky 9.9, task 4k4). For a
server that restarts on its own, that line comes back each time
the deploy lands in the gap: register it by `-pattern` instead
([chapter 4](04-register-bootstrap.md#choosing-the-target)):
in `~/.config/iormesh/servers.yaml`, replace the project's
`listen_port: N` under `target:` with `pattern: '…'` and deploy
again (a changed target, like changed flags, starts the session
anew). Look it up in
[troubleshooting.md](../troubleshooting.md#a-session-that-does-not-start);
deploy's own refusals (not bootstrapped, `sudo: a password is
required`, a refused upgrade) are under
[Deploy and the sudoers rule](../troubleshooting.md#deploy-and-the-sudoers-rule)
and [Signatures](../troubleshooting.md#signatures-and-pinned-keys).

**After a partial failure:** the hosts that deployed are already
tracing, the failed ones are not, and a connection between them
is seen from one end only (a `mixed` edge in
[chapter 6](06-collect-build.md#name-your-services)). Once the
failed hosts are fixed, start every session of the run again in
one command, before the traffic you want to trace:

> **Human step.** `-restart` stops and starts the running
> sessions too; an agent asks first, naming the hosts.

```sh
iormeshctl deploy -restart app1 app2
```

Without `-restart`, the hosts that did start keep their sessions
(`left alone`) and only the fixed ones start, minutes later; a
fixed host whose entry changed (a `pattern:` for a `listen_port:`)
starts anew either way. `rm` is not the way: it uninstalls the
tracer and unregisters the host. A restart keeps what the early
sessions already published, so connections made in the gap still
reach the next build: drop them as chapter 6's step 2 says.

## Check status

```sh
iormeshctl status            # every registered host and project
iormeshctl status app1       # one host
```

It changes nothing on the hosts. One row per host and project;
run on Rocky 9.9 (task 4k4) a minute after the deploy:

```text
$ iormeshctl status
TARGET  PROJECT  STATE    END-REASON  ENDS-BY               VERSION  UNCOLLECTED  NOTE
iom1    shop     running  -           2026-11-26T16:33:20Z  0.1.0    1
iom2    billing  running  -           2026-11-26T16:33:24Z  0.1.0    1
```

| Column | Meaning |
|---|---|
| `STATE` | `running`, `ended`, `dead` (the tracer died without its end), `none` (never started here), or `error` (the host could not be asked; the reason is in `NOTE`) |
| `END-REASON` | why an ended session ended: `duration`, `targets_exited`, `stop`, `disk_limit`, `error` |
| `ENDS-BY` | when the session's `-duration` runs out |
| `UNCOLLECTED` | segments waiting in the host's `ready/` |
| `NOTE` | e.g. `stopped by operator: ensure leaves it` |

**Checkpoint:** every row reads `running`. Now make the client
talk to the server, so there is something to see, and wait two
minutes (`sleep 120`): the tracer publishes a segment per minute.

On the host itself, `sudo /opt/iormesh/bin/iormesh status` prints
the same per project, and `journalctl -u iormesh-shop.service`
(as a user who may read the system journal: root, or a member
of `adm`, `systemd-journal` or `wheel`; the sudoers rule does not
cover it) is the session's log.

Reference: [architecture.md](../architecture.md) (deployment
modes, the session unit), [security.md](../security.md) (what the
unit may do), decision #19 in [decisions.md](../decisions.md)
(flags change only by stop and start).

---

← [4. Register hosts and bootstrap them](04-register-bootstrap.md) · [Contents](README.md) · Next: [6. Collect and build](06-collect-build.md) →

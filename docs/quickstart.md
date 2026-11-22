# Quickstart — a rendered mesh on two Rocky 9.9 hosts

From nothing to a mesh you can open in a browser: one client
service on `app1` talking to one server on `app2`, both traced,
fused into one picture on your laptop. Every command below is a
documented one; each step links to the doc that explains it.
When something fails, look the message up in
[troubleshooting.md](troubleshooting.md).

**Verification status.** Every command and flag here was checked
against the `-h` output of `iormeshctl` and `iormesh` built from
this tree, and `add`, `list` and the "nothing yet" answers of
`build`, `show` and `open` were run against a scratch registry.
The sequence itself has **not been run end to end as written**:
`bootstrap` (task qh4), `deploy`, `status`, `collect` and `ensure`
were not run against real hosts for this guide. The M4
acceptance runs on two Rocky 9.9 VMs
([testing.md](testing.md#m4-acceptance-on-two-real-hosts-task-lb4))
exercise the same `add`, `deploy`, `collect`, `refresh`, `build`,
`show` and `rm` path.

The example names: hosts `app1` and `app2`, SSH user `ops` on
both, project `shop` (a Java service on `app1`) and project
`billing` (whatever listens on TCP 8443 on `app2`). Substitute
your own.

## 1. Prerequisites

**The controller** (your laptop, Linux or macOS; it needs no
eBPF and no root):

- The repository, Go, [mage](https://magefile.org) ≥ 1.17, and
  Podman or Docker for the release builds
  ([build.md](build.md#container-builds)).
- `minisign`, once, to make the release key.
- OpenSSH with key-based login to both hosts through your agent:
  iormeshctl runs ssh with `BatchMode=yes`, so it never prompts.
- Both host keys already in `~/.ssh/known_hosts`
  (`StrictHostKeyChecking=yes`; log in once by hand and compare
  the fingerprint out of band).
- rsync 3.4.0 or newer first in `PATH` — macOS's own rsync or
  openrsync will not do ([security.md](security.md)).

**Each host** (Rocky Linux 9.9, x86_64; the arm64 tracer is
built but has never been loaded on a real arm64 kernel):

- The stock el9 kernel (5.14.0-…el9_9) with BTF at
  `/sys/kernel/btf/vmlinux` — Rocky ships it.
- systemd, `rsync` (`sudo dnf install -y rsync` on a minimal
  install), and a POSIX login shell for `ops`.
- `ops` may use `sudo` — once, for the first install. A password
  prompt is fine (step 4 shows how).
- `/opt` root-owned and not writable by others (the default).
- No firewall change: the tracer listens on nothing; everything
  goes through SSH.

## 2. Release key and signed builds

Hosts only run builds signed with your key
([build.md](build.md#signing), PLAN.md decision #28). Make the
key once, outside the repository:

```sh
mkdir -p ~/.config/iormesh ~/.minisign
minisign -G -p ~/.config/iormesh/minisign.pub -s ~/.minisign/iormesh.key
```

`~/.config/iormesh/minisign.pub` is where `iormeshctl` looks for
the pinned key by default (`-key` overrides). Then build, from
the repository root:

```sh
mage buildPodman      # or: mage buildDocker; tracers and controllers in dist/
mage agentBuild       # only if a project uses the Java agent
IORMESH_MINISIGN_KEY=~/.minisign/iormesh.key mage sign
```

Install the controller for your laptop and check a build with
it:

```sh
# or dist/iormeshctl-darwin-arm64, … for your laptop
install -m 0755 dist/iormeshctl-linux-amd64 ~/bin/iormeshctl
iormeshctl verify dist/iormesh-linux-amd64
```

Run the later `iormeshctl` commands from the repository root:
`bootstrap` and `deploy` read `dist/` there (`-dist` names another
directory).

## 3. Register the hosts

`add` only edits the registry
(`~/.config/iormesh/servers.yaml`); it contacts nothing. Flags of
`iormeshctl` go before HOST, tracer flags after it:

```sh
iormeshctl add -project shop -pattern 'java -jar */shop.jar*' app1 -duration 8h
iormeshctl add -project billing -listen-port 8443 app2 -duration 8h
iormeshctl list
```

`-pattern` is a glob over the whole command line (or the
executable path), matched at every exec and against the
processes already running at start, so it usually needs `*` at
both ends. `-listen-port` traces whoever owns that listening
socket when the session starts. `-duration` bounds the session
(4h by default, 7 days at most). [trace-scope.md](trace-scope.md)
says what gets traced. For Java stack attribution add
`-spool /run/iormesh/spool/shop -agent /opt/iormesh/lib/iormesh-agent.jar`
after HOST and read [java-attach.md](java-attach.md).

## 4. Bootstrap: the one-time install

`deploy` only upgrades a tracer that is already installed
through a narrow sudoers rule; the first install is your own
sudo, once per host (PLAN.md decision #1,
[security.md](security.md)). `bootstrap` verifies
`dist/iormesh-linux-<arch>` against the pinned key before
anything leaves the laptop, and prints or runs the exact steps:

```sh
iormeshctl bootstrap -user ops app1     # prints the steps, contacts nothing
```

Run the printed `ssh`/`scp` lines on the laptop and the rest on
`app1` as `ops` in a terminal, where sudo may ask for the
password. They install `/opt/iormesh/bin/iormesh`, the pinned key
`/opt/iormesh/etc/minisign.pub` and the rule
`ops ALL=(root) NOPASSWD: /opt/iormesh/bin/iormesh` as
`/etc/sudoers.d/iormesh`, checking every file's SHA-256 before it
goes into place. The last printed step,
`sudo -n /opt/iormesh/bin/iormesh status`, proves the rule.

If `ops` already has sudo without a password, let iormeshctl run
the same steps over SSH instead (it reads arch and user from the
host):

```sh
iormeshctl bootstrap -apply app2
```

`-arch arm64` prints the steps for an arm64 host; `-user` defaults
to HOST's `user@` part, else your own login name — a `User` in
`~/.ssh/config` is not seen, so name it.

## 5. Deploy

Start the services you want to trace on both hosts first (the
`shop` JVM on `app1`, the 8443 server on `app2`): a `-listen-port`
session refuses to start when nothing listens yet. Then:

```sh
iormeshctl deploy app1 app2
```

Per host, deploy checks `rsync` and `systemd-run`, verifies the
build against the pinned key, stages it, runs
`sudo -n /opt/iormesh/bin/iormesh upgrade` (a no-op for the same
bytes) and starts each project as a transient systemd unit
`iormesh-<project>.service`. A host that fails is reported and
the next one is still deployed. Deploying again with the same
build and flags leaves a running session alone; `-restart`
restarts it.

## 6. Check status

```sh
iormeshctl status
```

One row per host and project: `TARGET`, `PROJECT`, `STATE`
(`running`, `ended`, `dead`, `none`), `END-REASON`, `ENDS-BY`,
`VERSION`, `UNCOLLECTED` (segments waiting in `ready/`) and a
`NOTE`. It changes nothing on the hosts.

Now make the client talk to the server, so there is something to
see.

## 7. Collect

The tracer writes minute-rotated parquet segments to
`/opt/iormesh/traces/<project>/`; a finished minute moves into
`ready/`. After a minute or two:

```sh
iormeshctl collect
```

It moves every host's `ready/` segments into the local store
(`~/.local/share/iormesh/traces/<project>/<host>/`) over rsync
and prints `<host>: project <p>: N new files in <dir>` per
project. Collect as often as you like while the sessions run.

## 8. Build

```sh
iormeshctl build
```

`build` never contacts a server. It merges every project in the
store (`-project` picks some), fuses the two ends of each
cross-host connection, and prints the summary — `built`,
`merged … edges (N fused, …)`, `health …`, then the paths of
`graph.json`, `model.json`, `report.json` and the site.
`iormeshctl refresh` is collect then build in one go.

## 9. Look at it

```sh
iormeshctl open               # opens the newest site (xdg-open, open on macOS)
iormeshctl show               # the summary again, as text
iormeshctl show edges         # who talks to whom, fused or unresolved
iormeshctl show health        # loss, session ends, clock bounds
iormeshctl tui                # the same in the terminal
```

`show` also has `services`, `conversations`, `sequence [ID]`,
`thread`, `processes` and `report`; `-json` prints the view
model. [visualization.md](visualization.md) explains each view.
With both ends traced, `shop → billing` shows as one fused edge;
with only one side traced it stays unresolved.

## 10. Keep it running, then clean up

A session ends at its `-duration`, on a reboot or when the disk
watchdog trips, and is not restarted by itself. For runs over
several days, run `ensure` from a timer: per host it starts again
every session that ended on its own (never one you stopped with
`iormesh stop`), then collects:

```sh
iormeshctl ensure
```

[architecture.md](architecture.md#tracing-for-days) has the
systemd user timer and the cron line.

When you are done, `rm` stops each project, syncs its last
segments off and uninstalls `/opt/iormesh` with the sudoers rule
bootstrap installed, then unregisters the host:

```sh
iormeshctl rm app1
iormeshctl rm app2
```

## Next

- Pods instead of hosts: [kubernetes.md](kubernetes.md).
- Following connections to untraced peers:
  `iormeshctl expand` (PLAN.md M6, [security.md](security.md)).
- Naming services: `iormeshctl services name`.
- Querying events: `iormeshctl query`
  ([output-contract.md](output-contract.md)).
- `iormeshctl <subcommand> -h` prints each subcommand's flags.

# Troubleshooting

Common failures of a host deployment and what to do about them,
keyed by the text the tools print. The messages are quoted from
the error strings in `internal/ctl`, `internal/cli` and the
packages they call; `…` stands for the part that varies (a host,
a path, a number). Pods have their own table in
[kubernetes.md](kubernetes.md#troubleshooting). The first run is
walked through in [quickstart.md](quickstart.md).

The messages come from the source, not from a failing run on real
hosts; a few (`host not registered`, `pinned key`, the `add` usage
refusals, `nothing listens on TCP port`, the `build`, `show` and
`open` "nothing yet" answers, the `prune` and window refusals)
were reproduced locally. A message that reads differently in your
version: grep for its fixed part.

## Reading an error

- `iormeshctl` prints `iormeshctl: <error>` — for a wrong flag
  or argument `iormeshctl: usage: <error>`, the usage text and
  exit 2; deploy, collect
  and ensure name the host first
  (`iormeshctl: app1: <error>`) and then try the next host, and
  the command exits 1 at the end (`deploy failed on 1 of 2
  hosts: app1`).
- `iormeshctl status` reports a host it could not ask in that
  host's rows instead: `STATE error`, the reason in the `NOTE`
  column. It still asks the other hosts and ends with
  `status failed on N of M targets`.
- A remote command that failed reads
  `ssh <host> <command>: exit status N: <its stderr>`. The part
  after `exit status N:` is what ssh, sudo or the tracer said —
  that is the part to look up below.
- The tracer prints `iormesh: <error>`. A session `deploy`
  started runs as the systemd unit `iormesh-<project>.service`;
  its log is on the host:
  `journalctl -u iormesh-<project>.service` as a user who may
  read the system journal (root, or a member of `wheel`, `adm` or
  `systemd-journal`). The sudoers rule only lets `ops` run the
  tracer, not journalctl.
- `iormeshctl status` shows each session's state and end reason
  without changing anything, and in its NOTE column whether an
  ended or dead session was stopped by the operator, which
  `iormeshctl ensure` leaves stopped ([Tracing for
  days](architecture.md#tracing-for-days)); `iormeshctl show
  health` shows what a build saw (loss, session ends, clock
  bounds).

## SSH, rsync and the registry

| Message contains | Meaning and fix |
|---|---|
| `Host key verification failed.` | The host's key is not in `~/.ssh/known_hosts`, or it changed. iormeshctl runs ssh with `StrictHostKeyChecking=yes` and never trusts on first use: log in once by hand, compare the fingerprint out of band, accept it. |
| `Permission denied (publickey…)` | ssh runs with `BatchMode=yes`: no password or passphrase prompt. Load the key into your agent (`ssh-add`) and check `ssh <host> true` works without a prompt. |
| `(gave up on the host after 1h0m0s)` | A command on that host hung past the per-host hour; the other hosts were still handled. Look for a stuck sudo or a dead mount on the host. |
| `rsync not found on the server, deploy and collect need it` | `sudo dnf install -y rsync` on the host. |
| `systemd-run not found on the server` | The host has no systemd; iormesh needs it for its sessions. |
| `registry: host not registered: … (iormeshctl add it first)` | `bootstrap`, `deploy`, `collect` and `status` act only on registered hosts. `iormeshctl add` it, or check the spelling against `iormeshctl list`. |
| `registry: file or directory not private to its owner: … has mode … (no … bits allowed)` | The registry or its directory is writable by others, or owned by another user: `chmod go-w` it. iormeshctl refuses rather than repairs it, since the registry decides where your key goes. |
| `iormeshctl: usage: -pattern after HOST: iormeshctl flags go before HOST, and the registry entry sets the tracer's` | `add -project … -pattern … HOST [TRACER-FLAG...]`: `-project`, `-pattern`, `-listen-port` and `-registry` belong before HOST. |
| `trace store: … has mode …, writable by others (chmod go-w)` | The local store (`~/.local/share/iormesh/traces`) must be yours alone. |

## Bootstrap

| Message contains | Meaning and fix |
|---|---|
| `sudo -n failed (…): -apply needs the SSH user's own sudo without a password` | `ops` needs a password for sudo, and `-apply` has no terminal to type it in. Run `iormeshctl bootstrap -user ops HOST` without `-apply` and run the printed steps by hand. |
| `/etc/sudoers.d/iormesh already exists: the host is bootstrapped (iormeshctl deploy upgrades it) or another rule is there` | Bootstrap never overwrites a rule. Deploy instead; to start over, `iormeshctl rm HOST` (it removes the rule bootstrap installed) and add the host again. A rule laid down by hand stays; remove it with `sudo rm /etc/sudoers.d/iormesh`. |
| `-user …: the SSH user on the host is …` / `-arch …: the host is …` | `-apply` reads both from the host; drop the flag or fix it. |
| `SSH user name "…" cannot be named in a sudoers rule: pass -user` | The default user (HOST's `user@` part, else your login name) is not a valid name for the rule. A `User` from `~/.ssh/config` is not seen: pass `-user`. |
| `pinned key: open …: no such file or directory` | No pinned key at `~/.config/iormesh/minisign.pub` (or at `-key`). Make or copy it ([build.md](build.md#signing)). |
| a printed step fails with `install layout: …` | The tracer's own `upgrade` check refused the layout: `/opt` or `/opt/iormesh` is not root's or is writable by others. Fix the ownership; nothing after that step ran. |

With `-apply`, a failed step ends in `…; stopped here, nothing
undone`. Until the rule is in place a rerun starts over.

## Signatures and pinned keys

| Message contains | Meaning and fix |
|---|---|
| `local build, nothing pushed: signature verification failed: signed by key …, the pinned key is …` | `dist/` was signed with another key than the pinned one. Re-sign with your key (`mage sign`), or pin the right public key. Nothing left the laptop. |
| `signature verification failed: the file does not match its signature` | The file changed after signing (a rebuild without `mage sign`). Sign again. |
| `signature verification failed: … was signed as …` | A file renamed into place, e.g. one arch's tracer as another's. Use the file as built. |
| `artifact is not stamped with the version being signed: … rebuild it` (from `mage sign`) | A tracer, controller or jar in `dist/` is left from an older build. Rebuild `dist/` from the current commit, then sign. |
| `upgrade of iormesh-linux-… refused, nothing started: …` | The host's tracer refused the staged build; the reason follows (the rest of this table and the next). The host keeps what it ran. |
| `upgrade: refusing to downgrade from … to … (root may pass -allow-downgrade)` | `dist/` is older than the installed tracer. Build and sign the current version. |
| `upgrade: … is installed with other bytes; through the sudoers rule a new build must carry a new version` | A rebuild at the same version. Bump `internal/version.Version` for a release ([build.md](build.md#signing)), or reinstall as root on a test host. |
| `upgrade: iormesh-agent.jar … does not match the installed tracer …; upgrade the tracer first` | The jar in `dist/` is from another version than the tracer. Run `mage agentBuild` and `mage sign` together with the tracer build. |

## Deploy and the sudoers rule

| Message contains | Meaning and fix |
|---|---|
| `/opt/iormesh/bin/iormesh is not installed: deploy only upgrades an installed tracer` | The host was never bootstrapped (or `rm` uninstalled it): run `iormeshctl bootstrap` first. |
| `sudo: a password is required` | The sudoers rule is missing or names another user. Check `/etc/sudoers.d/iormesh` on the host names the SSH user deploy logs in as; bootstrap again if it is gone. |
| `… is not allowed through the sudoers rule (only run, start, stop, status, upgrade and uninstall are)` | Only the lifecycle subcommands pass the rule; run others as root directly. |
| `flag …: not allowed through the sudoers rule` / `flag -… …: a deployed session may only use …` | A tracer flag a confined session may not use (`-out`, `-cpuprofile`, `-ready`), or `-trace-dir`, `-spool` or `-agent` off its pinned path. `add` refuses these up front (`iormeshctl: usage: flag -cpuprofile: …`); at deploy they come from a hand-edited or older registry. Remove the project (`iormeshctl rm -project P HOST`) and add it again without them. |
| `session stopped, not restarted: start: …` | Deploy stopped a running session to restart it and the new start failed: the project has no session now. Fix the cause and deploy again; `ensure` will not restart it. |
| `project … already running this build with these flags, left alone (-restart restarts it)` | Not an error: re-deploying the same build changes nothing. |

## A session that does not start

| Message contains | Meaning and fix |
|---|---|
| `target spec: nothing listens on TCP port …` | A `-listen-port` session needs a listener at start. Start the server first, then deploy again. A restart of the server later in the session is followed ([next section](#a-port-session-after-a-restart)). |
| `target spec: only the service manager holds the listener (pid […]) on TCP port … (socket activation; the service is not running)` | systemd holds the socket of an idle socket-activated service and the session runs with `-listen-grace 0`, or the listener could not be pinned (a user manager whose cgroup is unreadable; the second error says so). systemd itself is never traced: it would follow every service it spawns (decision #23, [trace-scope.md](trace-scope.md)). With a grace (the default 2m) the session waits for the service instead; otherwise start the service (`systemctl start`, or a connection to the port), then deploy again. |
| `iormesh: port …: no socket-activated service started within …` (stderr, the session ends `targets_exited`) | The session started on a port only systemd held and no service holding that socket appeared within `-listen-grace`. Wake the service (one request) while the session waits, raise `-listen-grace`, or enroll it by `-pattern`. |
| `iormesh: port …: pid … holds it but is not the socket-activated service …` (stderr) | A process holds systemd's listener but does not run in a `.service` unit of that manager (a login shell, another manager's unit): it is not traced. The session keeps waiting for the real service. |
| `target spec: no visible process owns the listener on TCP port …` | The listener's owner is not visible in the tracer's `/proc` (another PID namespace, e.g. a container on the host network), or it exited between the reads. Trace it where it runs ([kubernetes.md](kubernetes.md)), or deploy again. |
| `usage: -listen-grace …: want 0 to 168h0m0s` | `-listen-grace` takes a duration up to 7 days; 0 ends the session as soon as the port's owners are gone. |
| `iormesh-….service exited before the tracer attached; see journalctl -u iormesh-….service` | The run failed before it attached. The unit's journal since the start is appended to the error when it can be read (at most 20 lines and 1536 bytes, the newest kept: the cause is logged last, e.g. `nothing listens on TCP port …`); `iormeshctl deploy` and `ensure` print it too, Go-quoted on one line. |
| `iormesh-….service ended at once (…)` | The run attached and ended immediately; the reason is in parentheses (`disk_limit`: the disk is already over the limit, see below). |
| `iormesh-….service has not attached after …; it keeps running` | Attaching is slow (many processes, a large `-agent` load). Check `iormeshctl status` again shortly. |
| `iormesh-….service is already …; stop it first (flags change by stop and start)` | A standalone `iormesh start` over a live session of the same project. `sudo iormesh stop -project P` first. |
| `SELinux is enforcing and … is labelled …: systemd may not exec a binary outside a bin_t or usr_t path` | A standalone `start` of a binary under `/root` or a home directory. Install it under `/usr/local/bin` or `/opt` and `restorecon` it. Deployed tracers live in `/opt/iormesh/bin` and are not affected. |

## A port session after a restart

A `-listen-port` session follows the port when its owners exit
and a new process takes it (decision #23,
[trace-scope.md](trace-scope.md)). The tracer prints these on its
log (`journalctl -u iormesh-<project>.service`):

| Message contains | Meaning and fix |
|---|---|
| `iormesh: port …: following new owner pid …` | Not an error: the restarted server was recognised and is traced from now on; it shows as a new process in the build. |
| `iormesh: port …: no new owner within …` | Nothing of the same program took the port within `-listen-grace` (2m by default): the session ends `targets_exited`, which `ensure` restarts — and that start fails with `nothing listens on TCP port …` until the server is back. For a slow restart give the registry entry a longer `-listen-grace` (`rm -project P HOST`, `add` again, `deploy`). |
| `iormesh: port …: pid … holds it but is not the traced program (executable, uid or cgroup differs); not following` | Another program, user or cgroup holds the port now, or an upgraded executable runs in another cgroup; it is left alone and named once. Across a container restart only the exact executable path is followed ([kubernetes.md](kubernetes.md)). If it is the program you want, `deploy -restart` traces it from a fresh start. |
| `iormesh: port …: the owner's executable was unreadable at start; not following the port` | The session could not record what its owner runs, so it cannot recognise a restart and ends with the owners. |

## BPF load and the el9 kernel

Rocky 9.9's 5.14.0-…el9_9 kernel runs the tracer, the root BPF
suites and the integration suite (PLAN.md, task nf4;
[testing.md](testing.md#root-suites-on-rocky-99-task-nf4-2026-11-21)).
The kernel version floors quoted for upstream kernels (6.2 for
the clock bound, 6.10 for the Java uprobes) do not describe el9,
which has backported what they name. A load failure on el9 is a
defect to report, not an expected gap (PLAN.md).

| Message contains | Meaning and fix |
|---|---|
| `load BPF object: …; BPF load refused; SELinux is enforcing and this process runs as …` | The tracer runs in a confined SELinux domain (`container_t` in a pod). See [kubernetes.md](kubernetes.md#selinux) and [security.md](security.md); the denial (`avc … tclass=bpf`) is in the raw `audit.log`, not always in `ausearch -m avc`. |
| `BPF load refused; active LSMs that can refuse BPF: …` | AppArmor, a BPF LSM program or kernel lockdown refused the load. Check `/sys/kernel/security/lsm` and the LSM's policy. |
| `BPF load refused; no LSM is confining it, check the capabilities (CAP_BPF and CAP_PERFMON, or CAP_SYS_ADMIN)` | The tracer did not run as root (or lacks the capabilities). Through the sudoers rule it does; a standalone run needs `sudo`. libbpf's own `RLIMIT_MEMLOCK` hint does not apply on 5.11+ and el9 kernels. |
| `load BPF object:` after libbpf's `BPF program load failed` and a verifier log | The verifier rejected a program. On el9 that is a defect: report it with the verifier log, `uname -r` and the tracer version (the last ones were tasks gf4 and hf4, [testing.md](testing.md#run-on-rocky-99-2026-11-21)). |
| the load fails without `/sys/kernel/btf/vmlinux` | The kernel has no BTF, which CO-RE needs. Every Rocky 9 kernel has it; a custom kernel needs `CONFIG_DEBUG_INFO_BTF=y`. |

A tracepoint missing on a host's kernel is skipped at runtime,
not a failure ([build.md](build.md)).

## Java agent

The tracer prints these on its log (`journalctl -u
iormesh-<project>.service`) and traces the process on without
Java attribution (decision #2, [java-attach.md](java-attach.md)).
A JVM running at start is tried once; one that starts later in
the session — a later `-pattern` match, a traced process that
execs `java`, a restarted `-listen-port` owner — is tried every
250 ms for up to 30 s from its exec (late attach,
[java-attach.md](java-attach.md#live-attach)) and named once.

| Message contains | Meaning and fix |
|---|---|
| `no Java attribution for pid …: … dynamic agent loading is disabled in the target JVM (start it with -XX:+EnableDynamicAgentLoading, or use -javaagent)` | The JVM was started with `-XX:-EnableDynamicAgentLoading`. Start it with `-javaagent:/opt/iormesh/lib/iormesh-agent.jar=spool=/run/iormesh/spool/<project>` instead ([java-attach.md](java-attach.md#on-hosts-iormeshctl-deploys-to)). |
| `the JVM runs with -XX:+DisableAttachMechanism` | No live attach possible: use `-javaagent`. |
| `the JVM has no SIGQUIT handler (still starting, or -Xrs)` | Started with `-Xrs`, or attached too early: use `-javaagent`. |
| `the JVM is in another PID namespace than the tracer` | A containerised JVM seen from the host. Trace it from inside the pod ([kubernetes.md](kubernetes.md)). |
| `pid … is not a JVM, no agent loaded` | The target is not a HotSpot JVM: nothing to attach; its syscalls are traced as usual. |
| `no Java attribution for pid …: … (start it with -javaagent instead)` | The agent could not be staged in the JVM's `/tmp` (owned by another user, or not sticky). Use `-javaagent`. |
| `no Java attribution for pid …: its JVM did not accept the agent within 30s (…)` | A late JVM did not get its SIGQUIT handler ready in time (a very slow start, or `-Xrs`). Use `-javaagent`. |
| `no Java attribution for pid …: it exited before its JVM accepted the agent` | A short-lived JVM; nothing to fix. |
| `64 processes wait for the agent already; pid … gets no agent, nor does any other process exec'd before one of them is done (start them with -javaagent)` | An exec storm filled the late-attach queue. The processes exec'd until a slot frees get no agent; later ones are tried again. Start such JVMs with `-javaagent`. |
| `agent loaded into pid …; its I/O before now stays unattributed` | Not an error: live attach never sees what happened before it — for a late JVM, its start-up. Give a JVM whose start matters `-javaagent` (e.g. through `JAVA_TOOL_OPTIONS`). |
| `agent loaded into pid …, but it wrote no ring …: no Java attribution (its reason, iormesh-agent: …, is on the JVM's stderr)` | The agent loaded but did not start (an unsupported JDK, an unwritable spool). The JVM's own stderr says why. |
| `agent loaded into pid …, but its ring could not be verified: the JVM is in another mount namespace …` | An `iormesh attach` into a containerised JVM; the line names the ways that work ([java-attach.md](java-attach.md)). |

## Ensure and stopped sessions

`iormeshctl ensure` prints one line per project,
`<host>: project <p>: <what it did>`
([architecture.md](architecture.md#tracing-for-days) has the
rules). The lines that leave a session down:

| Line ends in | Meaning and fix |
|---|---|
| `stopped by the operator, left stopped` | Someone ran `iormesh stop` on the host (the `status` NOTE reads `stopped by operator: ensure leaves it`). Intended: ensure never undoes a stop. `iormeshctl deploy` starts it again. |
| `ended (stop) on a tracer that cannot tell an operator stop from a reboot's, left stopped (deploy upgrades it)` | The host runs a tracer from before `operator_stop` (`status` NOTE: `stop origin unknown (tracer predates operator_stop): ensure leaves it`). `deploy` upgrades and starts it; from then on a reboot's end is restarted. |
| `no session here: deploy starts it, ensure only restarts` | The project is registered but was never deployed on the host. Run `deploy`. |
| `running outside its unit, left alone` | A run outside the systemd unit (an `iormesh run` by hand) holds the project. Stop it on the host if the unit should run instead. |
| `…, restart failed` | The start failed; the error printed for the host names the cause, with the unit's journal tail ([above](#a-session-that-does-not-start)). |
| `ended (disk_limit): collecting first` | Not an error: the restart waits for the collect ([next section](#disk-limit)). |
| `collect failed, not restarted` | A `disk_limit` end whose collect failed; the collect's error follows. |
| `no readable status after the collect, not restarted` / `status after collect, disk_limit ends not restarted: …` | The status read after the collect failed; run `ensure` again. |

A deploy restart whose start failed (`session stopped, not
restarted: start: …`) leaves the project marked stopped, so
ensure leaves it too; the next `deploy` starts it.

## Disk limit

The disk watchdog ends a session before the trace fills the
disk: by default at 90% use of the trace directory's partition or
under 256 MiB free (`-disk-limit`, `-disk-min-free`,
`-disk-max-bytes` on `iormesh run`).

| Where | What you see |
|---|---|
| `iormeshctl status` | `STATE ended`, `END-REASON disk_limit` |
| the unit's journal | `disk limit reached: … is …% used (limit 90%)`, or `… has … bytes free (floor …)` |
| `iormeshctl build` / `show` | `session    <project>/<host> <id>: ended disk_limit (the disk watchdog stopped the tracer)` |
| `iormeshctl ensure` | `ended (disk_limit): N segments still in ready/ after the collect, not restarted`, `ended (disk_limit): restart after collecting failed`, or `ended (disk_limit), restarted after collecting` |

Fix: collect (the collected segments leave the host), free space
on the partition holding `/opt/iormesh`, or adjust the limits on
the registry entry, then `deploy` (or let `ensure` restart it
once `ready/` is empty). A restart while the disk is still over
the limit is refused by the new run's own watchdog at its start,
so a timer never loops on a full disk
([architecture.md](architecture.md#tracing-for-days)).

## No events, nothing to see

| Message contains | Meaning and fix |
|---|---|
| `<host>: project …: no /opt/iormesh/traces/…/ready yet, nothing to collect` | No minute has completed yet, or the session never started. Wait a minute, check `iormeshctl status`. |
| `trace store: stat …/traces: no such file or directory` (build) or `trace store: open …/traces: no such file or directory` (show, open) | A fresh controller: nothing was ever collected, so the store does not exist yet. Run `iormeshctl collect` first. |
| `build: nothing collected in … yet (iormeshctl collect)` | The local store is empty: collect first. `show` and `open` without `-project` answer an empty store with this same build error. |
| `show: no build of … yet (iormeshctl build…)` / `open: no site of … yet (iormeshctl build)` | Build first; `show -project` and `open -project` read the build made with the same `-project` set. |
| a running session whose build shows no events of the app | The `-pattern` matches nothing. It is a glob over the whole command line as `ps -eo args` shows it (or the executable path), so it usually needs `*` at both ends; it never matches the 15-character comm. A process outside the pattern is not traced. |
| `STATE none`, `no session: never started here` | The project is registered but deploy never started it on that host. |
| `the tracer died without its end: its active minute was not published` | The tracer was killed (OOM at `MemoryMax`, a crash, power loss). `ensure` starts it again. |

## Loss and ring drops

The tracer never blocks the traced process: when its ring buffer
is full, records are dropped and counted, and every drop is an
explicit `drop` event in the trace ([schema.md](schema.md)).

| Where | What you see |
|---|---|
| the unit's journal, at the end | `iormesh: stopped (…); lost records N (kernel N), skipped runs N, unpaired exits N, unreadable paths N, unattributed aggregates N` |
| `iormeshctl show` | `health     N lost, N skipped runs, N unattributed, N attach gap; …` |
| `iormeshctl show health` | `loss       …`, then per session its segments, events and end |

- **lost**: records dropped because the ring was full. Trace
  fewer processes (a narrower `-pattern`), keep `-aggregate` on
  (the default), and leave out `-native-stacks` and
  `-trace-families all` unless needed. Spike S3 measured the
  budget ([s3-overhead.md](spikes/s3-overhead.md)).
- **skipped runs**: program runs the kernel skipped because
  another BPF program was running on that CPU. Host-wide and
  only possible loss — the count includes untraced processes.
  Small hosts with many tracers or other BPF tools see more.
- **attach gap**: what a pattern-matched process did between its
  exec and the tracer attaching to it. Expected once per match.
- **unattributed**: folded reads or writes no single descriptor
  explains.

A drop on a host can leave a connection without its handshake
or its data messages, so it may stay unresolved or give a
one-sided clock bound (next section).

## Unresolved edges and missing clock bounds

| Where | What you see and what to do |
|---|---|
| `show`, `show edges` | `unresolved <from> -> <to> (N of M connections)`: the other end was not traced, or not built together. Trace the peer (add a project on its host) and `build` both projects in one run (no `-project`, or both); `iormeshctl expand` lists untraced peers. |
| `show` | `no clock bounds`, `no two-sided clock bound`, or `… N pairs without a bound`: no fused connection between those hosts carried traffic in both directions in any window. The bound comes from traffic between traced processes on both hosts; a one-way exchange, or loss on one host, leaves it one-sided. |
| `query` output | `"bytes_partial":true` on a `sendmmsg` or `recvmmsg`: the tracer could not read every message's length, so `bytes` is a lower bound and the event carries no `tcp_seq`. That direction of the connection fails its stream check and gives no bound ([schema.md](schema.md)); other connections between the hosts still can. |
| `show health` | `clock      A ~ B: N windows, width …, N one-sided, N EMPTY (contradicting)`: an empty window means the edges contradict each other — a clock was stepped or slewed harder than the assumed 1 ppm drift. Check NTP on both hosts. |

Hosts skewed beyond the fusion window get neither a fused edge
nor a bound (PLAN.md M4).

## Windowed builds and prune

| Message contains | Meaning and fix |
|---|---|
| `usage: -last is a window of its own: give it or -from/-to, not both` | `build -last DUR` or `-from`/`-to`, not both. |
| `usage: -last …: want a positive duration (90m, 2h)` | `-last` takes a Go duration: `90m`, `2h`, `48h` (no `d`). |
| `usage: the window -from … -to … is empty` | `-from` is not before `-to` (`-to` is exclusive). A bare `HH:MM:SS` is UTC on the store's one date; a window across midnight needs dates (`2025-11-21T09:00:00` or RFC 3339). |
| `usage: -from … is a time of day, but the store's segments start on D1 .. D2 (UTC): give its date, e.g. …` | The store (`build` and `query` alike) spans more than one UTC day, so a bare `HH:MM:SS` could mean any of them, and would change meaning when `prune` deletes the oldest day. Give the date: `YYYY-MM-DDTHH:MM:SS` (UTC) or RFC 3339. A segment that does not read still counts with its name's date. |
| `build: no readable segment for … in the window … in … (0 bad)` | No collected segment overlaps the window: collect first, or check the window (UTC). |
| `federate: no segment collected in … yet` | A bare time of day in `-from` or `-to` needs a collected segment to fix its date: collect first, or give a date. |
| `show: no build of …~window yet (iormeshctl build -from T\|-to T\|-last DUR)` / `open: no site of …~window yet (…)` | `-windowed` reads the last windowed build of the same `-project` set (and `-namespace`): make one with `build -last` or `-from`/`-to`. |
| `usage: prune needs -older-than DUR, a positive duration (720h)` | `prune` has no default age. |
| `prune: nothing collected for project … in …` | That `-project` has no directory in the store: check the spelling against the store's directories. |
| `… segment files ended before …; prune again with -apply to delete them (the service database forgets them too)` | Not an error: without `-apply` prune only lists. |
| `prune: service database: …; nothing deleted` | `-apply` could not make the service database forget the files, so it deleted none. Fix the database error and run again; `-db` names the database if `build` used another one. |
| `prune: removeat …` after `deleted N segment files that ended before …` | The database forgot that file, but it could not be deleted; the next build imports it again. Fix its permissions and prune again. |

## Damaged segments

| Message contains | Meaning and fix |
|---|---|
| `… bad segments left out of the build (listed above)` and `bad <segment>: <error>` lines | Some collected files could not be read; the build used the rest and exits 1. A segment cut off by a crash is expected; repeated ones are worth a report. |
| `build: no readable segment for … in … (N bad)` | Every segment of that project was unreadable. |

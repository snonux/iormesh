# Controller side

Part of [M4 — controller](../m4.md), in [Implementation
guide](../../implementation.md).

Registry, `~/.config/iormesh/servers.yaml` (`$XDG_CONFIG_HOME`
honored; the same path on macOS), written by `iormeshctl add` and
`rm` (`internal/ctl`, task cb4):

```yaml
servers:
  - host: app1.example.org
    projects:
      - name: shop
        target: {pattern: "*java*orders-service*"}
        flags: ["-duration", "4h"]
      - name: billing
        target: {listen_port: 8443}
  - host: 10.0.4.4                 # enrolled by expansion (M6)
    hop: 1                         # rounds from a hand-added host
    projects:
      - name: shop
        target: {listen_port: 5432}
expand:                            # M6; absent = allow nothing
  allow:
    cidrs: [10.0.0.0/8]            # canonical, no host bits
    hosts: ["198.51.100.*"]         # address-text globs; prefer cidrs
  max_nodes: 16                    # registered hosts included
```

```sh
iormeshctl add -project shop -pattern '*java*orders-service*' \
    app1.example.org -duration 4h      # flags after HOST pass through
iormeshctl rm -project billing app1.example.org   # one project
iormeshctl rm app1.example.org                    # the whole host
```

- Every server has at least one project (#21: the project is a
  manual label kept with the entry); removing the last project
  drops the host. Host + project twice is an error, as is `rm` of
  an unknown host or project.
- A target is a pattern or a listening port, never PIDs: an entry
  outlives the processes. Tracer flags the entry or deploy own
  (`-project`, `-pattern`, `-listen-port`, `-pid`, `-out`) are
  refused after HOST — usually an iormeshctl flag in the wrong
  place. So is any flag every later deploy would refuse (task
  ji4): one the sudoers rule bars (`-out`, `-cpuprofile`,
  `-ready`), or `-trace-dir`/`-spool`/`-agent` off the path
  deploy pins. An older registry holding one still loads; deploy
  names the entry and contacts no host.
- Hosts are `[user@]name` from `[A-Za-z0-9._-]`, never starting
  with `-` (an ssh option); IPv6 literals need an ssh_config
  alias. Project labels follow the tracer's rule
  (`internal/project`).
- The file is written atomically (temp file, fsync, rename, fsync
  of the directory), 0600 in a 0700 directory. It is refused when
  it, or its directory, is someone else's or group- or
  other-writable, and when it is over 1 MiB. Pattern and flags
  may not hold control characters. Unknown keys are an
  error, not dropped: M6's `expand:` section (allowlist,
  `max_nodes`) and a host's `hop` are part of the schema (task
  sb4, `internal/federate/expandcfg.go`) rather than tolerated,
  so `add` never loses them.
- `rm` tears the deployment down before it edits the registry
  (task eb4; see `rm` after `collect` below).

`deploy`, as the commands it runs (all through one multiplexed
SSH connection per host; built by task db4, `internal/ctl/deploy.go`):

```sh
iormeshctl deploy [-project P] [-dist DIR] [-key FILE] [-restart] HOST...

ssh H uname -m                           # x86_64|aarch64 -> amd64|arm64
ssh H command -v rsync                   # preflight: fail fast (#12)
ssh H command -v systemd-run
ssh H test -x /opt/iormesh/bin/iormesh   # installed? else bootstrap, #1
# verify dist/iormesh-linux-$ARCH + .minisig locally (#28), then:
ssh H pwd                                # the home, e.g. /home/ops
ssh H mkdir -p -m 0700 /home/ops/.iormesh/staging
rsync -ct --timeout=60 \
    dist/iormesh-linux-$ARCH dist/iormesh-linux-$ARCH.minisig \
    H:/home/ops/.iormesh/staging/
ssh H sudo -n /opt/iormesh/bin/iormesh upgrade \
    /home/ops/.iormesh/staging/iormesh-linux-$ARCH
# only when a project of H has -spool or -agent (task kf4): the
# verified dist/iormesh-agent.jar(.minisig) went in the same rsync, and
ssh H sudo -n /opt/iormesh/bin/iormesh upgrade \
    /home/ops/.iormesh/staging/iormesh-agent.jar
# per project:
ssh H systemctl show --property=ActiveState --value -- iormesh-shop.service
ssh H sudo -n /opt/iormesh/bin/iormesh stop -project shop  # live, changed
ssh H sudo -n /opt/iormesh/bin/iormesh start -project shop \
    '-pattern=*java*orders-service*' -duration 4h
ssh H sudo -n /opt/iormesh/bin/iormesh status -project shop
```

- Every host on the command line must be registered (and carry
  `-project` if given) before any host is contacted, and every
  selected entry's flags must be ones the server's `start`
  accepts: no entry-owned flag (`-project`, `-pattern`, `-pid`,
  …, possible in a hand-edited registry), no `-out`,
  `-cpuprofile` or `-ready`, `-trace-dir`, `-spool` and `-agent`
  only at their pinned paths (`internal/cli/confine.go`). Hosts
  deploy one after the other; a failed host is reported and the
  next one still deployed, and the command then exits 1.
- First install on a host has no binary to `upgrade` with — that
  bootstrap is decision #1, the operator's own sudo or
  configuration management; deploy stops at the `test -x` with
  that message and never tries to install. `iormeshctl
  bootstrap HOST` (`internal/ctl/bootstrap.go`, task qh4) prints
  the steps, or runs them with `-apply` as `sudo -n` over the
  same Transport: preflight (`uname -m`, the tools, `sudo -n
  true`, `id -un`, no `/etc/sudoers.d/iormesh` yet), the build
  read once and verified (`release.ReadVerified`), one push of
  build, signature, key and rule into the staging directory,
  then per file `install` to a dot-named temporary, a
  `sh -c 'test "$(sha256sum < "$1")" = "$2  -"'` check, `visudo
  -cf` for the rule, and `mv -f -T` into place; the installed
  tracer's `upgrade` of the staged build before the rule, `sudo
  -n iormesh status` after it. One step list
  (`bootstrapSteps`) serves both modes; the paths and the rule
  come from `internal/hostlayout`.
- The build is verified on the controller before each host's
  push (per host, not once per arch, so a build replaced
  mid-run is caught at the next host):
  `release.VerifyFile` with the key `iormeshctl verify` pins
  (`~/.config/iormesh/minisign.pub`), which also requires the
  signed name to be `iormesh-linux-$ARCH` (no other arch's build
  renamed into place). A missing binary or signature, a bad or
  foreign signature: nothing pushed, nothing run under sudo.
- Staging is `~/.iormesh/staging` on the server, never `/tmp`
  or `/opt`, named absolute (the home from `pwd`, which must be
  of `[A-Za-z0-9._/-]`): the path is handed to sudo, and a
  relative one would rely on sudo keeping the working directory
  (`Defaults runcwd` changes it). rsync `-ct` without
  `-p`/`-o`/`-l` leaves the files the login user's own, which is
  what `upgrade` requires of a staged file. The staged files are
  kept; `-c` compares checksums, so the next push of the same
  build transfers nothing while a staged copy damaged in place
  is sent again.
- `iormesh upgrade F` verifies `F` and `F.minisig` again
  against `/opt/iormesh/etc/minisign.pub`
  (`internal/cli/upgrade.go`), the same build is a no-op there,
  and its refusals (older build, bad signature, layout) come
  back verbatim in deploy's error — and no project is started.
  `-allow-downgrade` is never passed.
- The agent jar (task kf4, `internal/ctl/deploy_agent.go`,
  `internal/cli/upgrade_agent.go`): verified locally like the
  tracer, and signed at the tracer's version, else nothing is
  pushed; upgraded after the tracer, by the new binary, which
  installs a file signed as `iormesh-agent.jar` at
  `/opt/iormesh/lib/iormesh-agent.jar` only at its own version
  (decisions.md #28, provisional). A host whose projects do not use
  the agent gets no jar. A tracer upgrade to another version
  removes the installed jar (it matched the old tracer); `start`
  forwards `-agent` as the checked real path and its unit re-checks
  it (`IORMESH_CONFINED_UNIT`).
- Under sudo runs only `/opt/iormesh/bin/iormesh` with `upgrade`,
  `start`, `stop` and `status`; `sudo -n` fails instead of
  prompting when the rule is missing. Every argv element is
  single-quoted by `SSH.Run`; the target is one
  `-pattern=`/`-listen-port=` element, so a pattern starting
  with `-` is no flag.
- Restart (#19): a live unit (`systemctl show`, unprivileged) is
  stopped and started again unless the controller's record
  (`~/.local/share/iormesh/deployed.json`, 0600: the start argv
  and build version deploy last launched per host and project)
  shows the same argv and version — then it is left alone,
  which makes a re-deploy of the same build and entry a no-op.
  A new build restarts the session: upgrade replaces the file,
  not the running process, and a deployed fix must not leave
  the old binary running. `-restart` restarts anyway. A failed
  stop (or one whose run did not end cleanly) starts nothing; a start
  failing after the stop is reported as "session stopped, not
  restarted". `iormeshctl rm` drops the host or project from
  the record too.
- Tests (`internal/ctl/deploy_test.go`): a fake `Transport`
  records every call's argv; exact sequences for two hosts of
  two arches, the idempotent re-deploy, `-restart`, a new build
  and a changed entry; refusals — unregistered host, unknown
  project or a flag the server refuses (no call at all),
  unknown arch, rsync missing, no installed tracer, an odd
  home, missing/tampered/foreign/renamed build (nothing pushed;
  checked per host), upgrade refused (no start), start failing
  (reported, the other project and host still deployed), stop
  or start-after-stop failing on a restart; `rm` pruning the
  record.

`status` and `list` (task ph4, `internal/ctl/status.go`,
`list.go`) change nothing anywhere:

```sh
iormeshctl status [-project P]... [-registry FILE] [K8S] [HOST|POD...]
ssh H sudo -n /opt/iormesh/bin/iormesh status -json   # once per host
# per pod of a pods: entry, per project (newest tracer container):
kubectl exec POD -c iormesh -- iormesh ready -project P -status
#   an image from before -status: ... ready -project P -state

iormeshctl list [-registry FILE]                       # contacts nothing
```

- `status` resolves its targets as `collect` does (every
  registered one by default; an unregistered one refused before
  any contact) and prints one row per target and project:
  state, end reason, ends-by (running sessions), the run's
  tracer version from `state.json` (`unknown` for an older
  tracer's state or image) and the events segments in `ready/`
  awaiting collection. A registered project the host does not
  know is `none`; a session the host reports that the registry
  does not list is shown with a note. `status -json` exits 1
  when one report carries an error but still prints them all,
  so the output is read whatever the exit. A host or pod that
  fails is a row saying why, the others are still asked, and
  the command exits 1. Each target runs under the per-host
  deadline (`env.onHost`). `status` is already in the sudoers
  rule's subcommands, so nothing is widened; a pod's tracer
  container has no systemd, so it is asked `iormesh ready
  -status` (status's JSON report without a unit) instead.
- `list` prints the registry: hosts (with an expand hop), pod
  entries (mode, container), each project with its target and
  pass-through flags.
- Every value either prints goes through `safe` (the oj4
  buildgate). Tests (`internal/ctl/status_test.go`): the exact
  host call, rows for running / none / unlisted / failed
  report / unreachable host, escaped control bytes, a pod with
  `-status`, an old image's `-state` fallback, a pod without a
  tracer, a pod gone; `list` for hosts and pods.

`ensure` (task zh4, `internal/ctl/ensure.go`) keeps sessions
going over days, from a timer
([architecture.md](../../architecture.md#tracing-for-days); the timer
in [the guide](../../guide/09-tracing-for-days.md#from-a-systemd-user-timer)):

```sh
iormeshctl ensure [-project P]... [-registry FILE] [-store DIR] [HOST|POD...]
ssh H sudo -n /opt/iormesh/bin/iormesh status -json    # hostReports
# per project that ended on its own (deployer.start, deploy's argv):
ssh H sudo -n /opt/iormesh/bin/iormesh start -project P ...
# then collect's calls for every selected project (collector.host)
# a disk_limit end: status -json again, start only if ready/ is empty
```

- The decision (`ensureDecide`) reads the report: a live unit
  or a running state is left; `dead`, a unit without a state,
  and an `ended` session restart — unless `operator_stop` is
  true, or the end is `stop` with no `operator_stop` (a tracer
  that predates it cannot tell a reboot from the operator), or
  it is `disk_limit`, which waits for the collect. A project
  with no report (never ran there) is deploy's. A report error
  is that project's failure; the host is still collected.
- A status that cannot be read fails the host before anything
  is started; a failed start or collect fails it after the
  rest ran; every host runs under `env.onHost`, and the command
  exits 1 naming the failed hosts. The deploy record is not
  touched (ensure does not know the installed build).
- A pod entry (task pk4, `internal/ctl/ensure_pods.go`) is
  decided per pod by its newest tracer container as the API
  server reports it (`podDecide`): a debug tracer that
  terminated non-zero is restarted through deploy's pod path
  (`deployPod`: next `iormesh-N`, attach wait), one that exited
  0 is left (a signal stopped it) and its collect skipped, a
  running one is asked `ready -status` (`podProjectStatus`) and
  an ended session there reported as needing a pod restart; no
  tracer container is deploy's, a sidecar the kubelet's. The
  pods are then read again and collected (`collector.pod`); a
  gone pod fails the entry, a terminating one (a `pod:` entry's
  `Get` returns it in its grace period) is left. `k8s.PodState`
  carries the exit code (`Exited`, `ExitCode`) and
  `Terminating`.
- Tests (`internal/ctl/ensure_test.go`): the exact calls for a
  restart, an operator stop and a running session; each
  decision; `disk_limit` collected-then-restarted, still full,
  collect failed; an unreachable host, a failed start, a report
  error; unregistered pod entry and host and a stray project
  refused with no call. Pods (`ensure_pods_test.go`, the fake
  kubectl): a killed tracer restarted as `iormesh-2` and
  collected through it, an exit-0 tracer left, a gone pod
  failed, a terminating pod left, running / lingering-ended /
  no-tracer pods left, a failed restart (the eighth tracer) in a
  selector entry whose next pod is still ensured and collected,
  a refused flag before any call. Tracer side
  (`internal/cli/operatorstop_test.go`): the mark before and
  after a stop, on an unclean stop, not outside the trace root
  through sudo, `false` from the start, absent in an old state.

`collect`, as the commands it runs (built by task eb4,
`internal/ctl/collect.go`; no sudo anywhere):

```sh
iormeshctl collect [-project P]... [-store DIR] [HOST...]

ssh H true                                   # reachable? else H fails
# per project:
ssh H test -d /opt/iormesh/traces/shop/ready # exit 1: never started, skip
rsync -rt --timeout=60 --fsync --remove-source-files --max-size=1073741824 \
    --exclude='.*' --include='*.parquet' --exclude='*' -e 'ssh …' -- \
    H:/opt/iormesh/traces/shop/ready/ \
    ~/.local/share/iormesh/traces/shop/H/
```

- No HOST collects the whole registry; `-project` repeats. A
  host or project the registry lacks — or a named host that
  carries none of the projects — fails before any host is
  contacted. A host named twice is collected once.
- Only `ready/` is fetched, and of it only the non-dot
  `*.parquet` files directly in it (`federate.FetchFlags`,
  shared by `SSH.Fetch` and the real-rsync test): a
  subdirectory, symlink or dot file planted there is neither
  pulled nor removed. The tracer writes the active
  segment as `.active-*` at the project directory's top and
  renames it into `ready/` whole and synced, tables before
  events, so rsync only ever sees final files; and
  `--remove-source-files` deletes a server file only after it
  transferred, and `--fsync` (rsync 3.2.4+) makes that mean
  fsynced on the controller, so a crash or power loss right
  after a collect cannot lose a segment the server already
  deleted (task ii4); rsync does not sync the local directory,
  so the new name itself is not yet covered. A file over 1 GiB
  (`--max-size`, the pod path's `k8s.MaxSegmentSize`) is skipped and stays on the server, so
  `rm` lists it as a leftover (task gi4). Collect keeps no
  state: run repeatedly during a live session, each run moves
  what was published since the last, and between runs the
  server holds only unsynced segments plus the active one.
  `ready/` is the deploying user's (`-ready-owner`), so reading
  and deleting there needs no privilege.
- Local layout, the build's input:
  `<store>/<project>/<host>/<fqdn>-<session>-<start-ts>-<seq>[.<table>].parquet`,
  store `~/.local/share/iormesh/traces`. One directory per
  registry host, not one per project: segment names come from
  the server, and a shared directory would let one host
  overwrite another's. Project and host are checked again
  (`project.Valid`, `federate.ValidHost`) before they become
  path components. The store and project directories are
  created 0700 and refused, not repaired, when foreign-owned
  or writable by others (whoever can write them could swap a
  host directory for a symlink), checked before anything is
  created inside them and, for the store, before any host is
  contacted; Fetch forces the host
  directory to 0700. The build reads `*.parquet` and skips
  dot files, rsync's in-flight temporaries.
- `test -d` answers "no ready/" only with exit status 1;
  anything else (ssh's 255) fails the project.
- Failures: an unreachable host or a failed transfer is
  reported by host and project; the other projects and hosts
  are still collected, and the command exits 1 naming the
  failed hosts. A partial transfer loses nothing: what moved
  is local, the rest stays in `ready/` for the next run.
- A hung host is a failed host. Each host runs under its own
  deadline derived from the caller's context
  (`env.onHost`, `defaultHostTimeout` = 1 h; deploy, rm and
  expand -apply use the same), and the error then says "gave
  up on the host after …". The hour only catches a remote
  command hanging on a live connection; a dead link is ssh's
  ServerAlive options' (about 45 s) and a stalled transfer
  rsync's `--timeout=60`, an I/O idle timeout that never cuts
  off a long transfer that is still moving.
- Pod entries likewise (task pi4): collect's and rm's entry,
  and in deploy the entry's pod lookup and each pod, run under
  `env.onHost`. Below the hour, internal/k8s bounds each
  kubectl call that answers and exits (`callTimeout`, 2 min:
  get, debug, the remove exec, `Run`) and gives the fetch
  stream an idle timeout (`streamIdle`, 2 min without a byte;
  `idleReader` in `exec.go`), both `ErrStalled`. An idle
  timeout rather than a whole-fetch one, for rsync's reason:
  a large backlog may stream for long, and only silence means
  a stalled exec — one that would otherwise hold the
  directory's fetch lock.
- Ctrl-C: `ctl.Main` runs every command under a root context
  from `signal.NotifyContext` (SIGINT, SIGTERM; `env.ctx`,
  `env.root`). The first signal cancels it — the ssh and
  kubectl calls end, deferred cleanups run (fetch lock,
  `kubectl debug`'s `--custom` temporary) — and the command's
  error reads "interrupted (interrupt signal received): …";
  the handler is then dropped, so a second Ctrl-C kills
  outright. Tests: `internal/k8s/bound_test.go` (a stalled
  stream, a slow one not cut, hung remove/get/debug) and
  `internal/ctl/interrupt_test.go` (hung pod deploy and rm, an
  interrupted collect, the signal context).
- Tests (`internal/ctl/collect_test.go`): exact call sequence
  over the whole registry, identical on a second run;
  `-project`/HOST selection and refusals with no call at all;
  unreachable host, failed fetch, missing `ready/` and a
  `test -d` failing with 255; a host that stalls given up on
  at the (shortened) per-host bound while the next is still
  collected, deployed or, for rm, the registry kept
  (`Test{Collect,Deploy,Rm}HungHost`); an open store, a store that is a
  file and an open project directory refused; and
  `TestCollectLiveSession`, real rsync with `FetchFlags` over
  a temp "server" with an active file at the project top,
  published segments in `ready/` and a planted symlink,
  subdirectory and dot file — two collects around a rotation
  move exactly the published segments, never the active file
  or the planted entries.

`rm`, as the commands it runs (`internal/ctl/rm.go`, task eb4):

```sh
iormeshctl rm [-project P] [-store DIR] [-force] HOST

ssh H true                               # unreachable: warn, unregister, tombstone
ssh H test -x /opt/iormesh/bin/iormesh   # exit 1: just unregister
# per removed project:
ssh H systemctl show --property=ActiveState --value -- iormesh-shop.service
ssh H sudo -n /opt/iormesh/bin/iormesh stop -project shop   # if live
# ... collect's test -d and rsync for the project ...
# only when the host leaves the registry:
ssh H test -d /opt/iormesh/traces
ssh H find /opt/iormesh/traces -mindepth 1 -maxdepth 1 -type d -print
# ... stop and collect every other project dir found ...
ssh H find /opt/iormesh/traces -mindepth 2 -type f \
    '(' -path '*/ready/*' -o -name '.active-*' ')' -print   # must be empty
ssh H sudo -n /opt/iormesh/bin/iormesh uninstall
```

- The stop comes before the sync so the last minute is
  published and collected too. `iormesh stop` fails when the
  unit is gone but `state.json` does not say `ended` (killed
  after `TimeoutStopSec`, or never attached): its active
  minute is unpublished, and rm stops there.
- `uninstall` runs only when the host leaves the registry
  (`rm HOST`, or its last project). It would stop and delete
  every project under `/opt/iormesh/traces`, so rm first
  stops and collects the other ones the registry or deploy's
  record (`deployed.json`) lists for the host. The directory
  names come from the server, so one neither lists (started by
  hand, or planted by a compromised server) is not collected —
  the server must not choose which project's store it writes
  into — and neither is a name that is no project label (it
  cannot be a local path component). Then whatever is still in
  a `ready/` or active, theirs included, refuses the uninstall,
  the files named (the first ten) and, apart, the unlisted
  project dirs that hold any of them.
- An unreachable host is only unregistered (the host, or the
  `-project`): its tracer and traces stay on it. The removed
  projects are then not forgotten from `deployed.json` but
  kept as tombstones (`"dropped": true`, no start or version),
  so a later `rm HOST` — the host still registered, or added
  again — collects them as its own instead of refusing over
  them. A tombstone never matches a deploy's last start (the
  project restarts if re-added); a successful `rm HOST`
  forgets the host, tombstones included.
- `test` answers "no" only with exit status 1; any other
  failure — ssh's 255 on a dropped connection — is an error,
  never "not installed" or "nothing published"
  (`remoteTest`). `Transport.Run` wraps the remote status in
  an `*exec.ExitError` for that.
- The registry and deploy's record change only after every
  step succeeded; a failed or unclean stop, a failed sync or
  check, leftovers or a failed uninstall exit 1 with the
  registry as it was and, before the uninstall, nothing
  uninstalled. `-force` turns an unclean stop and leftovers
  into warnings and uninstalls: the operator accepts losing
  those files. Rerunning rm is safe: a stopped session is not
  live any more, and what synced is local.
- Tests (`internal/ctl/rm_test.go`): exact sequences for
  `rm HOST` with a live session, `rm -project` keeping the
  host and removing its last project, the sweep of a project
  only deploy's record lists (an odd name skipped), a project
  dir nothing lists left uncollected and refusing without
  `-force` (`TestRmSweepSkipsForeign`), `rm -project` on an
  unreachable host then `rm HOST` collecting that project
  (`TestRmProjectUnreachableStaysKnown`); leftovers refusing
  and `-force` uninstalling; a failed sync, an unclean stop,
  `test -d`/`test -x` failing with 255, or a failed uninstall
  leaving the registry unchanged; `-force` past an unclean
  stop; the unreachable and the uninstalled host. In
  `internal/cli`, `TestStopUncleanEnd`.

Packages: `internal/federate/registry.go`, `transport.go` (the
`Transport` interface of M7, coded against from M4 on), `ssh.go`
(wraps the `ssh`/`rsync` binaries — no SSH library, the
operator's `~/.ssh/config` must just work), `merge.go`; the
commands in `internal/ctl` (`collect.go`, `rm.go`, which never
call the merge), `cmd/iormeshctl` only dispatching.

The SSH transport:

- `Run(ctx, argv...)` — `ssh [opts] -- HOST 'a' 'b' …`: ssh joins
  its command words into one string for the remote shell, so each
  argv element is single-quoted (`'` as `'\''`); an argument
  stays one argument and nothing in it expands remotely. That is
  POSIX quoting: the deploying user's login shell on a server
  must be a POSIX shell (csh/tcsh read `!` and newlines inside
  single quotes).
- `Fetch(ctx, remoteDir, localDir)` — `rsync -rt --timeout=60
  --fsync --remove-source-files --exclude=.* --include=*.parquet
  --exclude=* -e 'ssh [opts]' -- HOST:DIR/ LOCAL/`: a move of
  the non-dot segment files directly in DIR, each deleted
  remotely only once transferred and fsynced locally. `-rt`,
  not `-a`: without `-p` the server's modes never reach the
  controller (a 0755 `ready/` would open the local dir), and
  without `-l` a symlink the server planted is skipped, not
  recreated. The remote dir must be an absolute path from
  `[A-Za-z0-9._/-]` without `..`; the local dir is made absolute
  (no `host:` misread) and forced to 0700 after every fetch.
- Options on every call: `BatchMode=yes` (key or agent only, no
  prompt to hang on), `StrictHostKeyChecking=yes` (the host key
  must already be in known_hosts — no trust on first use, never
  `no`), `ControlMaster=auto`, `ControlPersist=60s` and
  `ControlPath=<dir>/%C` — one multiplexed connection per host;
  `ConnectTimeout=15`, `ServerAliveInterval=15` and
  `ServerAliveCountMax=3` — a silent connect fails after 15 s,
  a session that stops answering after about 45 s. Both rsync
  calls carry `--timeout=60` (no data for a minute ends them).
  The dir is `<user cache dir>/iormesh/ssh`, or `~/.iormesh/ssh`
  on macOS, whose `~/Library/Caches` leaves too little of a unix
  socket's 104-byte path; a dir too long for one is refused up
  front. It is 0700 and refused if it is a symlink, someone
  else's, or open to others.
- Tests record the argv through an injected runner, run the
  quoting through a local `sh`, and run `Fetch`'s real rsync
  flags between two local dirs (a 0755 source with a 0666 file
  and a symlink); nothing reaches a host.
  `internal/buildgate` checks iormeshctl links no cgo package and
  cross-compiles for darwin/arm64.

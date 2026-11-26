# M4 hosts, deploy and the sudoers rule

Part of [M4 — Controller (`iormeshctl`): push, collect, merge
locally](../m4.md), in [Milestones](../../milestones.md).

- `iormeshctl` runs on the controller — a laptop or MacBook,
  Linux or macOS — with no eBPF and no cgo at all
  (cross-compiled darwin/linux, amd64/arm64; the merge and
  render packages stay cgo-free).
- **Command-line conventions** (both binaries, `iormesh` and
  `iormeshctl`; task vh4): exit 0 on success, 1 when the command
  failed, 2 on a usage error (an unknown subcommand or flag, a
  missing argument — printed with the full usage); the tracer's
  `normalize -check` adds 3 for golden violations. `help`, `-h`
  and `--help` print the full usage to stdout and exit 0;
  `SUBCMD -h` (or `--help`) is a request too: it prints only that
  subcommand's flags (stderr, Go's flag format) and exits 0 —
  never 2, never followed by "flag: help requested" or the full
  usage. The full usage names every subcommand, verb, view and
  flag; a test holds `iormeshctl`'s text to its registered
  subcommands and flag sets, so it cannot drift.
- **Server registry**: `iormeshctl add HOSTNAME` / `rm HOSTNAME`
  manage the list automatically (kept in the controller's
  config, e.g. `~/.config/iormesh/servers.yaml`). Only
  registered hosts are ever deployed to (principle #3).
  Entries are project-aware: the same server can host several
  independent sessions, one per project.
- **Deploy**: detect the server's arch (amd64/arm64), verify
  the tracer build's signature on the controller (decision
  #28), push it over SSH into the deploying user's staging
  directory, install it as `/opt/iormesh/bin/iormesh` with
  `sudo iormesh upgrade`, and start it with `sudo iormesh
  start`. Every session is labeled with a project (`-project`),
  so completely different, non-interacting applications on the
  same server are traced and stored independently. Built (task
  db4): `iormeshctl deploy [-project P] HOST...` refuses an
  unregistered host before contacting any; stages in
  `~/.iormesh/staging` (never `/opt` or `/tmp`); never passes
  `-allow-downgrade`; an upgrade refusal starts nothing; a live
  session is restarted (stop, start; #19) when its start argv
  or build differs from the one the controller last launched
  (`~/.local/share/iormesh/deployed.json`) or with `-restart`,
  else left alone, so a re-deploy is a no-op. Deploy only
  upgrades: a host without the installed tracer is reported as
  needing the bootstrap of #1 (`iormeshctl bootstrap`), never
  installed. The Java agent
  jar ships on the same trust path (task kf4, #28): a host with
  a project that uses the agent (`-spool` or `-agent`) also gets
  the signed `dist/iormesh-agent.jar`, verified on the
  controller, staged with the tracer and installed by `sudo
  iormesh upgrade` as `/opt/iormesh/lib/iormesh-agent.jar`;
  every refusal starts nothing.
- **The tracer runs as a transient systemd unit**: `start`
  launches the session through `systemd-run` as
  `iormesh-<project>`. That gives it a life independent of the
  SSH session, cgroup CPU and memory caps (the enforced half of
  the overhead budget), logs in the journal, and a clean stop
  (`iormesh stop` → SIGTERM → flush the active segment, write
  the final event). Runs default to a 4h auto-exit
  (`-duration`, ior-style), with the unit's `RuntimeMaxSec` as
  a second, independent bound — no unbounded daemons on
  servers. Built (task 9b4): the caps default to one core
  (`CPUQuota=100%`) and 1 GiB (`MemoryMax=1G`), overridable
  with `start -cpu-quota`/`-memory-max`; `RuntimeMaxSec` is
  `-duration` + 5 min and `TimeoutStopSec` 30 s before systemd
  SIGKILLs. The run itself writes
  `<trace-dir>/<project>/state.json` (running once attached,
  then ended with its end reason); `start` waits for it, `status`
  reads it, and `start` refuses a project whose unit is live —
  flags change by stop and start (#19).
- **One sudoers rule**: every privileged action is a tracer
  subcommand — `start`, `stop`, `status`, `upgrade`,
  `uninstall` (and `run`, the foreground tracer `start`
  wraps) — so a
  single pinned rule for `/opt/iormesh/bin/iormesh` covers the
  whole lifecycle; nothing else on the server needs sudo. The
  binary is root-owned; how it gets installed and upgraded
  without turning that rule into full sudo is decision #1.
  Since every argument under the rule is the caller's, a
  session started through it is confined (task 9b4): `start`
  always, and `run` of the installed binary under sudo, refuse
  `-out`, `-cpuprofile` and `-ready`, pin `-trace-dir` to
  `/opt/iormesh/traces`, `-spool` to
  `/run/iormesh/spool/<project>` and `-agent` to the installed,
  signature-checked `/opt/iormesh/lib/iormesh-agent.jar` (task
  kf4), and give `ready/` only to the
  sudo caller; no existing directory is ever chmod'ed. Through
  the rule only `run`, `start`, `stop`, `status`, `upgrade`
  (signature-checked, decision #1) and `uninstall` run — every
  other subcommand is refused, fail closed
  ([docs/security.md](../../security.md) has the rule's text
  and the install's ownership requirements).

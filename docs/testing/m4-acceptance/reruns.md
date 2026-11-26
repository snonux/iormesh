# Reruns and the guide walk

Part of [M4 acceptance on two real hosts (task lb4)](../m4-acceptance.md),
in [Testing strategy](../../testing.md).

## M4 and M6 rerun at 9bca1b2 (task 3k4, 2026-11-26)

Both acceptances again on iom1, iom2 and iom3 (each Rocky 9.9,
5.14.0-741.22.1.el9_9.x86_64, JDK 25.0.5.1), with builds of
9bca1b2 (main's head): `mage containerBuild podman
tracer-amd64,ctl` and `mage agentBuild` into `dist/`, signed with
the test key (`mage sign`), `bin/iormeshctl` and `bin/workload.jar`
from the same tree. Since the last runs the controller gained
bootstrap, ensure, status/list, prune and windowed builds, and
deploy, collect, rm and federate changed in the audit fixes.

At the start every host had the 5eedf77 tracer installed (with its
key), a hand-laid sudoers rule without bootstrap's marker (so `rm`
leaves it) and `~/.iormesh/staging`; no unit, no trace, no
firewall port. Each install of the new build ran as root over SSH
(the printed bootstrap steps without the sudoers rule, which was
there), then `sudo -n iormesh upgrade` of the staged build through
the rule. The VMs were started one at a time; f3 never used swap.

**M4** (`mage m4Acceptance iom1 iom2`, the variables of "Run on
Rocky 9.9" above, work directory `~/.iormesh-m4-3k4-1`; 07:21:17
to 07:26:36 UTC, 5 min 19 s): **14 of 15 PASS, not accepted** —
M4-8's check fails, though the model meets the criterion (task
7m4).

| Id | Result |
|---|---|
| M4-0 | PASS — both amd64, 5.14.0-741.22.1.el9_9.x86_64 |
| M4-1 | PASS — 14 projects running on iom1 and iom2 |
| M4-2 | PASS — 3 rounds, 48 files collected and deleted on the servers |
| M4-3 | PASS |
| M4-4 | PASS |
| M4-5 | PASS — the tcp-loopback golden on 1475 events |
| M4-6 | PASS — one conn fused across iom1 and iom2; the client alone unresolved |
| M4-7 | PASS — client :18880, server :18203 |
| M4-8 | FAIL — "orders (:18213) not among the services" (task 7m4) |
| M4-9 | PASS — two conns fused across iom1 and iom2; `10.20.0.141:18216 tls x1, 10.20.0.141:18214 tls x1, 10.20.0.140:18215 plain x2` |
| M4-10 | PASS — one window, two-sided, width 947µs |
| M4-11 | PASS — byte-identical under `unshare -rn` |
| M4-12 | PASS |
| M4-13 | PASS — `ended disk_limit`, the segment intact |
| M4-14 | PASS — 15 sessions collected, nothing under `/opt/iormesh` |

M4-8 is a regression of the runner, not of the tracer or the
merge: since task ai4 (b496ff9) a loopback endpoint's label ends in
`@host` (`127.0.0.1:18213@a425…`), and the runner's
`serviceByPort` still matches the label's `:PORT` suffix, so it
finds none of the loopback services. The refresh's model holds the
criterion: `m4-orders-svc` (127.0.0.1:18213) `tls` x1,
127.0.0.1:18211 `tls` x1, 127.0.0.1:18212 `plain` x2, every edge
to them fused, 0 unresolved. Task 7m4 fixes the check.

**M6** on three hosts, wg4's procedure and layout above
(gateway and inventory with the agent on iom1, orders on iom2,
billing on iom3, iom3's 18214 open to iom2 alone), scratch home
`~/.iormesh-m6-3k4`; rounds 07:28:33 to 07:35:15 UTC, the
by-hand run (`~/.iormesh-m6-3k4h`) 07:35:42 to 07:37:29. **Every
criterion PASS.**

| Criterion | Result |
|---|---|
| only the gateway host enrolled, rounds discover and deploy every service host | PASS — round 1 proposes exactly `10.20.0.141:18216 hop 1` (one inbound skip aside); round 2's `expand -apply` enrolls and deploys iom2 at hop 1, bound to orders' pid; round 3's `expand -hops 2 -apply` enrolls `10.20.0.142` (`hop 2`, `listen-port 18214`) and starts the session on iom3 over SSH, bound to billing's pid |
| a hop limit of 1 stops at the first ring | PASS — after round 2, `expand -hops 1` lists `10.20.0.142:18214 hop_limit` ("hop 2, limit 1", via 10.20.0.141 java); `-apply` prints "nothing to enroll", the registry's hash unchanged |
| a peer outside the allowlist is listed, never contacted | PASS — with 10.20.0.142/32 dropped, `expand -hops 2` lists billing `not_allowed`, `-apply` enrolls nothing, the hash unchanged; iom3's sshd logged no login from round 2's last step (07:30:44) to the check (07:32:44), across `collect`, `refresh`, both `-hops 1` and both narrowed `-hops 2` runs |
| after the scenario re-runs, the merge equals the one of all hosts added by hand | PASS — round 3's own sessions merge to 4 edges fused, tcp 3 fused, 0 unresolved, orders `tls`, inventory `plain` x2, billing `-` (no agent on that hop); the by-hand run builds the same, and both graphs' connection shapes are identical (8 sockets; the one-ended ones a DNS socket to 10.20.0.1:53 on iom2 and on iom3 and an address-less accept in each agent JVM on iom1, in both) |
| convergence honesty | PASS — the store's merge over the three rounds keeps round 1's gateway → orders end and round 2's orders → billing end unresolved (`mixed`, 1 of 3 and 1 of 2), and round 1's server half of orders' untraced call to inventory (`(untraced)` → 18215) |
| a cyclic topology terminates | PASS — after round 3, `expand -hops 2`, `-hops 5` and `-hops 5 -apply` propose nothing (both peers `enrolled`), the hash unchanged |

The two-host M6 layout (3f4) was not run again: the three-host
run covers each of its criteria.

Afterwards every host was removed with `rm` from both scratch
homes (every session collected), the build of 9bca1b2 installed
again as at the start (the 5eedf77 tracer is not restored), the
run directories, ports, rich rule and DNAT removed, and the three
VMs stopped.


## Guide walk on Rocky 9.9 (task 4k4, 2026-11-26)

The user guide ([guide/](../../guide/README.md), chapters 2–10) walked
end to end on `iom1` and `iom2` (as above; 08:19 to 08:53 UTC),
with the commands as the chapters write them, then M4 once more.
Builds of c10b0a3: `mage containerBuild podman tracer-amd64,ctl`
and `mage agentBuild` into `dist/`, signed with a throwaway key
made by `minisign -G` (`aead.dev/minisign/cmd/minisign@v0.3.0`;
the laptop has no minisign package) through
`dist/iormeshctl-linux-amd64 sign -key … .`; the controller
installed from `dist/` as `~/bin/iormeshctl`. iormeshctl ran with
`HOME` set to a scratch directory, so the key, registry, store
and service database were the walk's own; the operator's were
not touched.

**Start and end state.** Both hosts had 3k4's state: the 9bca1b2
tracer (sha256 6674249a…) and the 3k4 test key in
`/opt/iormesh`, the hand-laid rule `paul ALL=(root) NOPASSWD:
/opt/iormesh/bin/iormesh` without bootstrap's marker, the staged
9bca1b2 builds in `~paul/.iormesh/staging`; no unit, no port. To
start from the state chapter 4 assumes, `sudo -n iormesh
uninstall` (it left the unmarked rule, as documented), then the
rule and `~/.iormesh` removed as root. At the end the same bytes,
modes and rule were put back as root, the hashes compared, and
the VMs stopped.

**Workload.** A 1.9 KiB `shop.jar` (a JDK HttpClient calling
billing every 3 s over keep-alive and writing an order record
through a temporary file and a rename) on `iom1`, and billing as
`java -m jdk.httpserver -b 10.20.0.141 -p 8443` on `iom2`, 8443
opened runtime-only for the walk.

**Bootstrap.** The SSH user `paul` has no password and no sudo of
its own, so the by-hand path on `iom1` ran the printed controller
lines on the laptop, the `printf` lines as `paul`, and the `sudo`
lines as root over SSH with `HOME=/home/paul`; the staged
`upgrade` needs the caller to own the staged file, so that one
line ran with `SUDO_UID=1000` — in a root shell it refuses
(`unsafe file: … is owned by uid 1000, not by the caller`, now in
the guide and troubleshooting.md). The checkpoint, `sudo -n
/opt/iormesh/bin/iormesh status` as `paul`, printed `iormesh: no
sessions`. On `iom2`, `bootstrap -apply` without sudo refused as
documented; with the temporary rule `/etc/sudoers.d/zz-4k4-temp`
(`paul ALL=(ALL) NOPASSWD: ALL`, 0440, added at 08:30:15 UTC and
removed at 08:30:43 right after the bootstrap; `sudo -n true` as
`paul` then failed again) it ran every step and ended
`bootstrapped; iormeshctl deploy iom2 starts its projects`; a
rerun refused with `/etc/sudoers.d/iormesh already exists`.

**Walked**, each as the chapter shows it: `add` (and its two
refusals), `list`, `bootstrap`, `deploy` (a `-listen-port` with
nothing listening, both hosts, a no-op redeploy), `status`,
`collect`, `build`, `services`/`services name`/`rules`/`unname`,
every `show` view, `show -json`, `tui` without a terminal, the
site folder (CSP `default-src 'none'`, no remote reference; no
browser was opened), `query` (`-event`, `-host`, `-file`, `-from`/
`-to`, `-conn`, `-errno`), the agent added to `shop` (deploy
pushed the jar, the running JVM got it by live attach, "37
natives probed", conversations appeared), `ensure` twice
(`shortshop`, `-duration 1m`, ended `duration` and was restarted;
`billing`, stopped with `sudo iormesh stop`, was left stopped and
marked `stopped by operator: ensure leaves it`; the second run
only collected; `deploy` started billing again), `build -last 2m`
and `-from`/`-to`, `show -windowed`, the two `-last` refusals,
`prune -older-than` listing then `-apply`, flags changed in the
registry by hand and `deploy` (it restarted the session),
`expand` and `expand -apply` (iom2 bootstrapped again, untraced,
allowed as `10.20.0.141/32`: enrolled at hop 1 and deployed bound
to billing's pid; the next `expand` lists it `enrolled`), and
`rm` (`rm iom1`, `rm -project billing iom2`; both removed
`/opt/iormesh` and the bootstrap rule).

**Findings, fixed in the guide and troubleshooting.md:**

- A connection already open when its session starts makes no
  connection and no edge (only reads and writes on its socket):
  the first build after deploying over the running keep-alive
  client was `0 services, 0 edges` from 726 events; with the
  client restarted it fused at once. A product gap, task cm4
  (priority H); chapter 5 said so, chapter 10's run shows the
  resulting `mixed` edge. Task cm4 closed it (decision #35,
  "Connections open before the session" below).
- `rm -project` of a host's last project uninstalls the host, so
  chapter 8's "rm -project, then add again" to add `-agent` left
  `iom1` without a tracer (deploy: `/opt/iormesh/bin/iormesh is
  not installed`). Chapter 8 now edits the registry's flags and
  deploys again; chapter 9 and troubleshooting.md say what rm
  does to a host's last project.
- `services name 'iom2*'` named nothing: the glob matches the
  name the client asked for or the IP, never the registry's host
  name; a client dialing an IP needs an IP glob (chapter 6,
  troubleshooting.md).
- Without the agent there are no http events, so no
  conversations (`show sequence`: `no such conversation: the
  build holds none (no http events)`), chapters 6 and 7.
- `mkdir -p ~/.config/iormesh` (chapter 3) made the registry's
  directory 0755, not the 0700 chapter 4 promised; chapter 3 now
  uses `mkdir -p -m 0700`.
- A long `HOME` breaks every ssh with `control socket path under
  … would be 191 bytes, over the 104 a unix socket allows`
  (`XDG_CACHE_HOME` moves it), chapter 4 and troubleshooting.md.
- `sudo -v` (chapter 2) succeeds for a user whose only rule is
  iormesh's narrow one; the check is `sudo -l` now.
- Chapter 5's deploy and status output, chapter 6's collect,
  chapter 9's ensure, prune and rm, and chapter 10's `expand
  -apply` are now captured from this run. rm leaves the SSH
  user's `~/.iormesh/staging` and an empty `/run/iormesh/spool`
  (chapter 9).
- deploy prints a failed unit's journal tail Go-quoted on one
  line (as troubleshooting.md said); readability task dm4.

**M4** (`mage m4Acceptance iom1 iom2`, the procedure and variables
of "Run on Rocky 9.9" above, `IORMESH_M4_KEY` the walk's
throwaway key, work directory `~/.iormesh-m4-4k4`; both hosts
bootstrapped again by hand as above; 08:42:41 to 08:48:12 UTC):
**every criterion PASS — M4 is accepted on el9.**

| Id | Result |
|---|---|
| M4-0 | PASS — both amd64, 5.14.0-741.22.1.el9_9.x86_64 |
| M4-1 | PASS — 14 projects running on iom1 and iom2 |
| M4-2 | PASS — 3 rounds, 43 files collected and deleted on the servers |
| M4-3 | PASS |
| M4-4 | PASS |
| M4-5 | PASS — the tcp-loopback golden on 1485 events |
| M4-6 | PASS — one conn fused across iom1 and iom2; the client alone unresolved |
| M4-7 | PASS — client :18880, server :18203 |
| M4-8 | PASS — all hops fused: `127.0.0.1:18213@a425… tls x1, 127.0.0.1:18211@a425… tls x1, 127.0.0.1:18212@a425… plain x2` (task 7m4's fix) |
| M4-9 | PASS — two conns fused across iom1 and iom2; `10.20.0.141:18216 tls x1, 10.20.0.141:18214 tls x1, 10.20.0.140:18215 plain x2` |
| M4-10 | PASS — one window, two-sided, width 750µs |
| M4-11 | PASS — byte-identical under `unshare -rn` |
| M4-12 | PASS |
| M4-13 | PASS — `ended disk_limit`, the segment intact |
| M4-14 | PASS — 15 sessions collected, nothing under `/opt/iormesh` |

Afterwards the ports and the DNAT were removed, the scratch homes
and the run directory deleted on the laptop, the 3k4 state put
back on both hosts, and both VMs stopped (`doas vm list`: `iom1`,
`iom2`, `iom3` Stopped; only `rocky` running).

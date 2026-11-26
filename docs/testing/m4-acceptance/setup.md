# Prerequisites and running it

Part of [M4 acceptance on two real hosts (task lb4)](../m4-acceptance.md),
in [Testing strategy](../../testing.md).

## Prerequisites

The controller (Linux or macOS):

- The repo with Go, a JDK (`mage workloadBuild`) and mage; the
  target builds the workload jar and a cgo-free iormeshctl
  itself.
- rsync 3.4.0 or newer first in `PATH`, or a patched
  distribution build (decisions.md #39: the preflight reads its
  rpm or Debian changelog for the backported fixes); it refuses
  others and openrsync ([security.md](../../security.md)).
- Key-based SSH to both hosts through the agent (`BatchMode`),
  each host key already in `~/.ssh/known_hosts`
  (`StrictHostKeyChecking=yes`; compare the fingerprint out of
  band, never accept it blind).
- A release key the operator makes once with minisign, outside
  the tree and never committed (`mage sign` refuses a key
  inside it):

  ```sh
  minisign -G -p ~/.config/iormesh/minisign.pub \
              -s ~/.minisign/iormesh.key
  ```

- A signed build for each host's arch and the signed agent jar
  in `dist/` (deploy ships the jar to both hosts: the
  microservices projects trace with the agent, task kf4):

  ```sh
  mage buildPodman      # or buildDocker; dist/iormesh-linux-*
  mage agentBuild       # dist/iormesh-agent.jar
  IORMESH_MINISIGN_KEY=~/.minisign/iormesh.key mage sign
  ```

Each host (two machines, Linux, x86_64 or aarch64):

- Kernel with BTF (`/sys/kernel/btf/vmlinux`) and the classic
  `raw_syscalls` tracepoints. 6.2 or newer is needed for the
  clock bound (M4-10: `tcp_seq` reads use `bpf_rdonly_cast`).
  The Java mark uprobes need 6.10 (or 6.6–6.9 with commit
  46ba0e49); the microservices projects load the agent (task
  kf4). The dispatcher findings (output-contract.md, "Syscall handlers run
  behind `raw_syscalls` dispatchers") were measured on Linux 7.2.
  Rocky 9.9's 5.14.0-741.22.1.el9_9 has backported the BPF
  features these floors name (`bpf_rdonly_cast`, uprobe_multi
  links with 46ba0e49's process-wide pid filter) and runs the
  tracer since tasks gf4 and hf4, and the root BPF and
  integration suites since task nf4 (see "Run on Rocky 9.9" and
  "Root suites on Rocky 9.9" below); the version floors above do
  not describe el9.
- systemd, rsync, Java 21 or newer on the SSH user's `PATH` (the
  goldens were confirmed on JDK 25), `ss`, GNU `df` and `stat`,
  `fallocate`, `timeout`, `pkill`, a POSIX login shell.
- HOST2 reachable from HOST1 on TCP `PortBase+2` (18202) and,
  with NAT, `PortBase+3` (18203): the prerouting redirect
  rewrites the port before firewalld's input filter sees the
  packet, so it is 18203 that must be open, not the DNAT'ed
  port. Open them for this run only — `firewall-cmd
  --add-port=18202/tcp --add-port=18203/tcp` without
  `--permanent` is runtime-only, gone at the next reload, and
  `--remove-port` with the same arguments closes them at once.
  The microservices spread (M4-9) also needs HOST2's orders on
  `PortBase+16` (18216) open to HOST1 and HOST1's inventory on
  `PortBase+15` (18215) open to HOST2; `base64` on both hosts
  copies orders' certificate.
- At least 1 GiB free on the partition of `/opt/iormesh` (HOST2
  fills 128 MiB of it for the watchdog trip).
- Idle: no `iormesh-*` unit loaded and nothing under
  `/opt/iormesh/traces` — the run ends with `rm`, which
  uninstalls `/opt/iormesh`, so it refuses a host whose traces
  are not its own.
- Bootstrapped (decision #1: the first install is the
  operator's own sudo) with `iormeshctl bootstrap` (task qh4),
  `ops` standing for the SSH user and `ARCH` for the host's
  `amd64` or `arm64`. It verifies the build against the pinned
  key before anything leaves the controller — the first
  install has no installed `upgrade` to check it — and checks
  every installed file's SHA-256 before it is renamed into
  place ([security.md](../../security.md)):

  ```sh
  # bin/iormeshctl from `mage build`, or
  # CGO_ENABLED=0 go build -o bin/iormeshctl ./cmd/iormeshctl
  # bootstrap contacts only registered hosts; a scratch registry
  # keeps the operator's own clean (pass it to bootstrap too)
  r=~/iormesh-boot/servers.yaml
  bin/iormeshctl add -registry "$r" -project boot -listen-port 1 HOST
  # print the exact steps and run them on HOST by hand (sudo may
  # ask for the password there):
  bin/iormeshctl bootstrap -registry "$r" -arch ARCH -user ops HOST
  # or, when ops has sudo without a password, run them over ssh:
  bin/iormeshctl bootstrap -registry "$r" -apply HOST
  ```

  Both install the rule
  `ops ALL=(root) NOPASSWD: /opt/iormesh/bin/iormesh` as
  `/etc/sudoers.d/iormesh`, under bootstrap's marker line.
  `/opt` itself must be root-owned and not writable by others
  (the steps' `upgrade` check refuses otherwise, before the
  rule goes in). The run's `rm` uninstalls `/opt/iormesh` and,
  with it, the rule bootstrap installed (a rule without the
  marker, laid down by hand before task qh4, stays: `sudo rm
  /etc/sudoers.d/iormesh` removes it), so the next run
  bootstraps the hosts again.
- For the NAT criterion, a DNAT on HOST2 from a port of the
  operator's choice to the NAT server's `PortBase+3`, the way
  `docker -p` would do it (removed again with `sudo nft delete
  table ip iormesh_m4`). `redirect` rewrites the destination to
  the primary address of the interface the packet came in on,
  so `IORMESH_M4_ADDR2` (where the server binds and the client
  dials) must be that address; for a secondary address use
  `dnat to ADDR2:18203` instead:

  ```sh
  sudo nft add table ip iormesh_m4
  sudo nft 'add chain ip iormesh_m4 pre { type nat hook prerouting priority dstnat; }'
  sudo nft add rule ip iormesh_m4 pre tcp dport 18880 \
       redirect to :18203
  ```


## Running it

```sh
export IORMESH_M4_NAT_PORT=18880
LIBBPFGO=../libbpfgo mage m4Acceptance app1.example.org app2.example.org
```

| Variable | Default | Meaning |
|---|---|---|
| `IORMESH_M4_ADDR1`, `_ADDR2` | `getent ahostsv4` on the other host | each host's IPv4 as the other reaches it (the servers bind `_ADDR2`, and the spread's inventory `_ADDR1`, so both must be addresses of the hosts' own interfaces) |
| `IORMESH_M4_NAT_PORT` | unset: M4-7 SKIP | HOST2's DNAT'ed port |
| `IORMESH_M4_DIST` | `dist` | signed builds |
| `IORMESH_M4_KEY` | `~/.config/iormesh/minisign.pub` | the pinned release key, copied into the work directory |
| `IORMESH_M4_WORKDIR` | `~/.iormesh-m4` | iormeshctl's `HOME`; must be empty or absent; kept afterwards (store, builds, service database) |
| `IORMESH_M4_PORT_BASE` | 18200 | workload ports are base+1 … base+16 |
| `IORMESH_M4_ROUNDS` | 3 | streaming collect rounds (2–10), each after a 65 s rotation |
| `IORMESH_M4_FILL_DIR` | the run's dir or `/var/tmp` | watchdog fill file's directory on HOST2; must be on `/opt/iormesh`'s filesystem |

A run takes about ten minutes and prints every command it
issues, then one result line per criterion and a verdict. Exit
status 0 only when every criterion passed; M4-9, the
microservices spread over both hosts, runs since task 1f4. Every
workload role
runs under `timeout 300`, and a failed scenario's servers are
killed, so a broken client cannot hang the run. The run's own
files on the servers (`~/.iormesh-m4-run`: the jar and the
scenarios' `--dir` trees) are removed at the end; deploy's
staging directory (`~/.iormesh/staging`) stays, as after any
deploy. **Because `rm` is a criterion, a finished run leaves
the hosts without `/opt/iormesh`: bootstrap them again before a
rerun**, and move the old work directory away.

The first ^C cancels the current step and still runs the
teardown (`rm` of both hosts, leftover roles killed, the run's
directory and fill file removed; at most 5 minutes); a second
^C ends the run at once (exit 130). mage's own handler would
exit 5 seconds after the first ^C, so the target takes SIGINT
away from it (`acceptance.InterruptContext`, task mi4) and the
teardown runs to the end in the same process, results table
included. If the teardown did not finish, clean up by hand —
with an iormeshctl built as above, and the operator's XDG
variables cleared so it reads the run's registry:

```sh
env -u XDG_CONFIG_HOME -u XDG_DATA_HOME HOME=$HOME/.iormesh-m4 \
    bin/iormeshctl rm HOST                  # per host
ssh HOST pkill -f .iormesh-m4-run/workload.jar
ssh HOST rm -rf .iormesh-m4-run            # HOST2 also: rm -f /var/tmp/.iormesh-m4-fill
```

Every session also ends by itself after its `-duration 1h`.

Send back the results block and the work directory's path.


## What the run checks

| Id | Criterion (milestones.md M4) | How | Expected |
|---|---|---|---|
| M4-0 | — preflight | ssh `pwd`; arch, kernel, machine ids (distinct, neither the controller's); `test -f` of the binary and key; `sudo -n -l /opt/iormesh/bin/iormesh`; the tools; no unit or trace; signed build per arch and signed `dist/iormesh-agent.jar`; local rsync; jar pushed | PASS, arch and kernel per host printed |
| M4-1 | `add` → `deploy`, zero manual steps on the servers | `add -project P -pattern 'java -jar */.iormesh-m4-run/workload.jar <scenario> --role <role> *' HOST -duration 1h` per role, `deploy HOST1 HOST2`, then `status -json` per project | every project `running` |
| M4-2 | streaming collect | per round: `file-basic` on HOST1, 65 s, list every `ready/`, `collect`, list again | every listed file gone from the server and in the store; the project dir holds only `ready/`, `state.json`, `.lock` and `.active-*`; ≥ 1 file moved |
| M4-3 | `collect` alone changes nothing but the local store | hash every file of the work dir outside the store before and after each `collect` | unchanged |
| M4-4 | `refresh` produces the merged graph export | `refresh` (no host, no project) | exit 0; every project and both hosts in the model; no damaged segment |
| M4-5 | merged mesh equals the single-host result | `build -project m4single`, `query -project m4single`, tcp-loopback's golden (`internal/normalize`) on the rebuilt graph and events | no violation (the rig checks graph equality on every run) |
| M4-6 | M0 client and server as two projects fuse | `build` of m4shop+m4billing (`tcp-two-host`, client HOST1, server HOST2); `build` of m4shop alone | one fused cross-host connection, none unresolved; alone, the client's end unresolved |
| M4-7 | NAT'ed pair fuses on different 4-tuples | `tcp-nat` through the DNAT; `build` m4natc+m4nats; `query -conn` | one fused connection; client dialed the NAT port, server accepted on base+3 |
| M4-8 | microservices: every REST edge fused, plain and TLS | the four services on HOST1, one project each, read from the refresh's model; each project added with `-spool /run/iormesh/spool/P -agent /opt/iormesh/lib/iormesh-agent.jar` (so deploy installs the signed jar), each JVM started with `JAVA_TOOL_OPTIONS=-javaagent:/opt/iormesh/lib/iormesh-agent.jar=spool=/run/iormesh/spool/P` — the TLS label comes from the agent's http records (task kf4) | orders and billing `tls`, inventory `plain` (2 conns), all fused |
| M4-9 | microservices spread over both hosts (task 1f4) | with the agent as in M4-8 (task kf4); gateway and inventory (`--bind ADDR1`, base+15) on HOST1, orders (base+16) and billing (base+14) on HOST2 (`--bind ADDR2`), one project each (m4sgw, m4sinv, m4sord, m4sbill), every pattern ending in its `--dir` `microservices-spread`, so M4-8's sessions never trace them; orders' public certificate copied from HOST2 to HOST1 over ssh (`base64`); `build` of the four projects | two fused cross-host connections (gateway→orders TLS, orders→inventory plain), none unresolved; orders and billing `tls`, inventory `plain` (2 conns), all fused |
| M4-10 | clock bound | the m4shop+m4billing model's clock windows | non-empty and two-sided, widths printed (real clocks; the injected skew is `TestClockSkewRecovered`'s) |
| M4-11 | `build` with the network down is byte-identical | both outputs deleted, then the same build rerun under `unshare -rn` (else with a `PATH` lacking ssh and rsync) | `graph.json` and `model.json` byte-equal |
| M4-12 | naming two services shows them by name | `services name -port 18202 ADDR2 m4-billing-svc`, `services name -port 18213 127.0.0.1 m4-orders-svc`, `build`, `services` | both names in the model's services and edges and in the listing |
| M4-13 | disk watchdog | `add` m4disk on HOST2 with `-disk-limit 0 -disk-min-free <free − 64 MiB>`, `deploy -project m4disk`, `fallocate` 128 MiB, wait | `ended`/`disk_limit`; collected segment intact; `show`'s summary prints `ended disk_limit` |
| M4-14 | `rm` leaves nothing under `/opt/iormesh` | `rm HOST1`, `rm HOST2`, `test -e /opt/iormesh`, units, registry, `build` of every project | no `/opt/iormesh`, no unit, empty registry; every session's end in the store |

Not run by the target: decision #25's tracer-CPU item (met,
task ee4, measured locally) and the injected clock skew (the
rig's, task ye4).

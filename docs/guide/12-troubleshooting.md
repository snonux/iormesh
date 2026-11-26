# 12. When things go wrong

Every error iormesh prints names what failed and, mostly, what to
do. This chapter is the order to look in, and the fix for each
symptom the path of chapters 2 to 8 meets. Everything else —
every message with its meaning, keyed by the text the tools
print — is in [troubleshooting.md](../troubleshooting.md), the
depth behind this chapter; this chapter is enough to finish the
path without it.

## Read the error

- **`iormeshctl: usage: …`** (exit 2) — a wrong flag or
  argument; the usage text follows. Most often an iormeshctl flag
  after HOST in `add` (chapter 4).
- **`iormeshctl: app1: …`** — deploy, collect and ensure name the
  host, go on with the next one, and exit 1 at the end (`deploy
  failed on 1 of 2 hosts: app1`).
- **`ssh <host> <command>: exit status N: …`** — the part after
  `exit status N:` is what ssh, sudo or the tracer said; look that
  part up.
- **`iormesh: …`** — the tracer's own message, from a printed
  step or relayed by deploy.

## Then look, in this order

1. **`iormeshctl status`** — is the session `running`, `ended`
   (and why), `dead`, `none`? A host it could not ask shows
   `STATE error` with the reason in `NOTE`. It changes nothing.
2. **The session's journal on the host** — `journalctl -u
   iormesh-<project>.service`, as root or a member of `wheel`,
   `adm` or `systemd-journal` (the sudoers rule lets `ops` run the
   tracer, not journalctl). Agent attach, port following and the
   disk watchdog log there. `deploy` and `ensure` already print
   the journal's tail when a start fails.
3. **`iormeshctl show health`** — what a build saw: lost records,
   attach gaps, how each session ended, clock bounds.
4. **[On the path](#on-the-path)**, below — find the message's
   fixed part; then [troubleshooting.md](../troubleshooting.md)
   if you have it. A message that reads differently in your
   version: grep the source for its fixed part.

## On the path

What chapters 2 to 8 most often run into, in the order they meet
it; `…` stands for the part that varies.

| Symptom | Fix |
|---|---|
| `tar: command not found` (chapter 3, on the laptop) | A minimal Rocky 9 has no tar: `sudo dnf install -y tar` (a controller prerequisite, [chapter 2](02-prerequisites.md)). |
| `rsync --version` older than 3.4.0 on the laptop | Fine if it is a distribution build with the fixes backported: run [chapter 2](02-prerequisites.md)'s one-line check for your distribution (Rocky 9.9's `rsync-3.2.7-1.el9_8` passes). Otherwise update the distribution's package; never build rsync from an unnamed source. |
| `Host key verification failed.` | The host's key is not in `~/.ssh/known_hosts`, or it changed. A human logs in once by hand and compares the fingerprint out of band ([chapter 2](02-prerequisites.md)); an agent never accepts it. |
| `Permission denied (publickey…)` | No prompt is possible (`BatchMode=yes`): load the key into the agent (`ssh-add`) until `ssh -o BatchMode=yes app1 true` exits 0. |
| `rsync not found on the server, deploy and collect need it` | `sudo dnf install -y rsync` on the host. |
| `pinned key: open …: no such file or directory` | No key at `~/.config/iormesh/minisign.pub`: make it or copy it ([chapter 3](03-release.md)). |
| `signature verification failed: …` | The file changed after signing, or was signed with another key than the pinned one: sign the release again with your key ([chapter 3](03-release.md)). Nothing left the laptop. |
| `sudo -n failed (…): -apply needs the SSH user's own sudo without a password` | `ops` needs a password for sudo: run `bootstrap` without `-apply` and let the human run the printed steps ([chapter 4](04-register-bootstrap.md)). |
| `/etc/sudoers.d/iormesh already exists: …` | The host is bootstrapped already: deploy instead. |
| `/opt/iormesh/bin/iormesh is not installed: deploy only upgrades an installed tracer` | Bootstrap the host first ([chapter 4](04-register-bootstrap.md)). |
| `sudo: a password is required` (deploy) | The sudoers rule is missing or names another user than the one deploy logs in as: bootstrap again. |
| `target spec: nothing listens on TCP port …` | A `-listen-port` session needs the server listening at its start: start the server, then deploy again. |
| `iormesh-….service exited before the tracer attached; …` | The cause is the last journal line printed under the error (often `nothing listens on TCP port …`); fix it, deploy again. |
| `no /opt/iormesh/traces/…/ready yet, nothing to collect`, or `0 new files` | No minute has completed yet: wait a minute or two and collect again; check `iormeshctl status` says `running`. |
| `build: nothing collected in … yet (iormeshctl collect)`, `trace store: … no such file or directory` | Nothing was collected yet: collect first. |
| `show: no build of … yet` / `open: no site of … yet` | Build first, with the same `-project` set. |
| a build with no events of the app | `-pattern` matches nothing: it is a glob over the whole command line as `ps -eo args` shows it, so it usually needs `*` at both ends. |
| events of the app, but `0 services, 0 edges` or `unresolved` edges | The app moved no TCP bytes yet, or its peer is not traced: trace the peer too and build both projects together ([chapter 6](06-collect-build.md), [chapter 10](10-expand.md)). |

On anything else, stop, quote the error, and look in
[troubleshooting.md](../troubleshooting.md) or ask the human.

## Depth, by symptom

The sections of [troubleshooting.md](../troubleshooting.md) that
hold the rest:

| Symptom | Section |
|---|---|
| `Host key verification failed.`, `Permission denied (publickey…)` | [SSH, rsync and the registry](../troubleshooting.md#ssh-rsync-and-the-registry) |
| a refused signature or upgrade | [Signatures and pinned keys](../troubleshooting.md#signatures-and-pinned-keys) |
| `bootstrap` fails | [Bootstrap](../troubleshooting.md#bootstrap) |
| `deploy` fails before a session starts | [Deploy and the sudoers rule](../troubleshooting.md#deploy-and-the-sudoers-rule) |
| a session that does not start or ends at once | [A session that does not start](../troubleshooting.md#a-session-that-does-not-start) |
| a server restarted under a port session | [A port session after a restart](../troubleshooting.md#a-port-session-after-a-restart) |
| `load BPF object`, a verifier log | [BPF load and the el9 kernel](../troubleshooting.md#bpf-load-and-the-el9-kernel) |
| no Java stacks | [Java agent](../troubleshooting.md#java-agent) |
| ensure leaves a session down | [Ensure and stopped sessions](../troubleshooting.md#ensure-and-stopped-sessions) |
| `END-REASON disk_limit` | [Disk limit](../troubleshooting.md#disk-limit) |
| an empty build, nothing collected | [No events, nothing to see](../troubleshooting.md#no-events-nothing-to-see) |
| `lost` or `skipped runs` above 0 | [Loss and ring drops](../troubleshooting.md#loss-and-ring-drops) |
| unresolved edges, no clock bound | [Unresolved edges and missing clock bounds](../troubleshooting.md#unresolved-edges-and-missing-clock-bounds) |
| `-from`/`-to`/`-last`, `prune` refusals | [Windowed builds and prune](../troubleshooting.md#windowed-builds-and-prune) |
| `bad segment` lines, build exits 1 | [Damaged segments](../troubleshooting.md#damaged-segments) |
| anything in a pod | [kubernetes.md, Troubleshooting](../kubernetes.md#troubleshooting) |

On Rocky 9.9's el9 kernel a BPF load failure is a defect to
report — with the verifier log, `uname -r` and the tracer
version — not an expected gap.

---

← [11. Kubernetes](11-kubernetes.md) · [Contents](README.md) · Next: [13. For agents working with a human](13-agents.md) →

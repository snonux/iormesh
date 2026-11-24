# 12. When things go wrong

Every error iormesh prints names what failed and, mostly, what to
do. This chapter is the order to look in; the messages
themselves, with their fixes, are in
[troubleshooting.md](../troubleshooting.md), keyed by the text the
tools print.

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
4. **[troubleshooting.md](../troubleshooting.md)** — find the
   message's fixed part. A message that reads differently in your
   version: grep the source for its fixed part.

## Where to look, by symptom

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

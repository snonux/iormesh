# 9. Tracing for days

A session ends at its `-duration`, on a reboot, when its targets
are gone, when its tracer is killed, or when the disk watchdog
trips, and nothing restarts it by itself. This chapter keeps
sessions going with `ensure` from a timer, builds a window of a
long store, trims the store, and finally cleans up.

## Keep sessions running: `ensure`

```sh
iormeshctl ensure                # every registered host and pod entry
iormeshctl ensure -project shop app1
```

Per host, ensure reads each session's status, starts again every
session that ended on its own (`duration`, `targets_exited`,
`error`, a reboot, a crash), then collects. It never upgrades,
never starts a project the host never ran, and never undoes an
operator's stop, so it is safe from a timer. It prints one line
per project, as the source writes it, for example:

```text
app1: project shop: running, nothing to do
app1: project shop: ended (duration), restarted
app1: project shop: stopped by the operator, left stopped
```

Two ends it treats differently:

- **A session stopped by hand** (`sudo /opt/iormesh/bin/iormesh
  stop -project shop` on the host) stays stopped; `status` marks
  it `stopped by operator: ensure leaves it`. `deploy` starts it
  again.
- **A `disk_limit` end** restarts only after the collect emptied
  its `ready/`; otherwise ensure says how many segments are still
  there.

The full rule, pods included, is the table in
[architecture.md](../architecture.md#tracing-for-days); the lines
that leave a session down are in
[troubleshooting.md](../troubleshooting.md#ensure-and-stopped-sessions).

### From a systemd user timer

Run again, ensure finds what it started running and only
collects, so a timer may drive it. The timer runs without you, so
it needs the SSH key without a prompt: an agent socket in the
user manager's environment (`systemctl --user
import-environment SSH_AUTH_SOCK`), or a key for this alone. With
pod entries registered it also needs kubectl, the cluster's
credential plugins and their kubeconfig (`systemctl --user
import-environment KUBECONFIG PATH`, or `Environment=` lines in
the service).

```ini
# ~/.config/systemd/user/iormesh-ensure.service
[Unit]
Description=iormeshctl ensure: restart ended iormesh sessions, collect

[Service]
Type=oneshot
ExecStart=%h/bin/iormeshctl ensure

# ~/.config/systemd/user/iormesh-ensure.timer
[Unit]
Description=Run iormeshctl ensure every 15 minutes

[Timer]
OnCalendar=*:0/15
Persistent=true

[Install]
WantedBy=timers.target
```

```sh
systemctl --user daemon-reload
systemctl --user enable --now iormesh-ensure.timer
journalctl --user -u iormesh-ensure.service   # what each run did
```

A timer never starts a run while the last one is still going (a
slow collect). With cron, `flock` gives the same:

```cron
*/15 * * * * flock -n ~/.cache/iormesh-ensure.lock ~/bin/iormeshctl ensure >>~/.cache/iormesh-ensure.log 2>&1
```

ensure exits 1 when a host or pod entry could not be reached or a
start or collect failed, after trying every target; the next run
retries. It only keeps the segments coming: run `build` (or
`refresh`) when you want the picture.

> **Human step.** A timer keeps root sessions running on
> production hosts for days. The host owners agree to it; the
> human installs the timer.

## Build a window

After days of tracing, a build of everything is slow and mixes
old with new. Build only a window instead; the full build stays,
and `-windowed` reads the window's build:

```sh
iormeshctl build -last 2h                     # or -from 09:00:00 -to 11:00:00
iormeshctl show -windowed                     # open -windowed, tui -windowed alike
```

`-from` and `-to` take RFC 3339, `YYYY-MM-DDTHH:MM:SS` (UTC), or a
bare `HH:MM:SS` (UTC) while the store spans one UTC day; on a
longer store give the date. `-to` is exclusive, and `-last DUR`
(`90m`, `2h`, `48h`; no `d`) goes alone. A window keeps the whole
segments that overlap it, and its services and edges are the
connections seen in it. The build prints a `window` line before
its paths, and the site and the TUI title name the window.

Reference: [visualization.md](../visualization.md#namespace-and-windowed-builds),
[troubleshooting.md](../troubleshooting.md#windowed-builds-and-prune)
(the refusals).

## Trim the store: `prune`

The store only grows. `prune` lists the segment files whose range
ended more than `-older-than` ago and deletes them only with
`-apply`, which first makes the service database forget them
(their request counts, and the connections seen only in them;
naming rules stay):

```sh
iormeshctl prune -older-than 720h          # lists, deletes nothing
iormeshctl prune -older-than 720h -apply
```

Nothing is ever pruned on its own. `-project` narrows it; `-db`
names the service database if `build` used another one.

> **Human step.** `-apply` deletes trace data for good. Show the
> list first; the human says yes.

## Clean up

When you are done with a host, `rm` stops each project, syncs its
last segments off, uninstalls `/opt/iormesh` together with the
sudoers rule bootstrap installed, then unregisters the host:

```sh
iormeshctl rm app1
iormeshctl rm -project billing app2      # one project only
```

rm refuses to uninstall over traces it could not sync off;
`-force` uninstalls anyway and loses them. The collected traces
stay in your store until you delete them.

> **Human step.** `rm` uninstalls from a production host, and
> `-force` loses data. Ask first.

Reference: [security.md](../security.md) (what rm removes and
what it refuses).

---

← [8. Java code flow and query](08-java-query.md) · [Contents](README.md) · Next: [10. Grow the mesh](10-expand.md) →

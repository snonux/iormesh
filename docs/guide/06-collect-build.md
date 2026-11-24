# 6. Collect and build

`collect` moves the finished segments from every host into the
store on your laptop. `build` merges the store into one mesh,
fuses the two ends of each cross-host connection, and writes the
graph, the view model, the rollup and a local site. `refresh` is
both in one go.

## Collect

```sh
iormeshctl collect               # every registered host
iormeshctl collect app1          # one host
```

Over rsync, collect takes each host's `ready/` segments into
`~/.local/share/iormesh/traces/<project>/<host>/` and removes them
from the host. It prints one line per host and project, as the
source writes it:

```text
app1: project shop: 3 new files in ~/.local/share/iormesh/traces/shop/app1
```

Collect as often as you like while the sessions run; segments
left on a host count against its disk limit.

**If not:** `app1: project shop: no /opt/iormesh/traces/shop/ready
yet, nothing to collect` means no minute has finished yet, or the
session never started: wait a minute and check `status`
([chapter 5](05-deploy-status.md)).

## Build

```sh
iormeshctl build                 # every project in the store
iormeshctl build -project shop   # only these (repeatable)
```

`build` never contacts a server. It prints the summary, then the
paths it wrote. A test golden from a two-host fixture with the
projects `billing` and `shop`
(`internal/ctl/testdata/build_summary.golden`):

```text
built      billing+shop: 2 hosts, 2 sessions, 8 segments, 22 events, 2025-11-20 00:53:20 - 2025-11-20 00:53:20 UTC
merged     3 services, 2 edges (1 fused, 0 mixed, 1 unresolved); tcp 1 fused, 1 unresolved
health     0 lost, 0 skipped runs, 0 unattributed, 0 attach gap; clock bounds <= 740µs
clock      hostA.example.org ~ hostB.example.org: 1 windows, width 740µs-740µs
sequence   6 conversations, 12 steps (9 time-ordered, 0 unordered)
session    shop/app1 a1b2c3d4: no end recorded (running, or cut short)
unresolved java -> 203.0.113.9:5432 (1 of 1 connections)

graph      $BUILDS/billing+shop/graph.json
model      $BUILDS/billing+shop/model.json
report     $BUILDS/billing+shop/report.json
site       $BUILDS/billing+shop/sites/$RUN/index.html
           iormeshctl open  ·  iormeshctl show
```

How to read it:

- **`merged`** — `fused` edges had both ends traced and joined;
  an `unresolved` one had only one end traced. With `shop` and
  `billing` both traced and built together, `shop → billing`
  shows as one fused edge.
- **`health`** — `lost` counts records dropped because a ring
  buffer was full; `clock bounds` are how far apart two hosts'
  clocks can be, proved by traffic between them.
- **`session`** — a session to look at: one with no recorded end
  (still running is fine) or an unusual end such as `disk_limit`.
- **`unresolved`** — a peer no tracer saw. Trace it
  ([chapter 10](10-expand.md)) or leave it.

`$BUILDS` is `~/.local/share/iormesh/builds`; a build of several
projects is named after all of them (`billing+shop`). Each run's
site gets its own folder, kept until you delete it.

**Checkpoint:** `build` exits 0 and prints a `site` line. It exits
1 when it had to leave a damaged segment out (`… bad segments left
out of the build (listed above)`); the rest is still built.

**If not:** on a fresh laptop, run for this guide:

```text
$ iormeshctl build
iormeshctl: trace store: stat ~/.local/share/iormesh/traces: no such file or directory
[exit status 1]
```

Nothing was ever collected: run `collect` first. `build: nothing
collected in … yet` says the same for an empty store.

## Refresh

```sh
iormeshctl refresh
```

`refresh` is `collect`, then `build`, with the same `-project` and
host selection. It is the command you repeat while sessions run.

## Name your services

A server is named after its address until you name it. Name it
once, by a glob over the endpoint's host name or IP address and a
port, and every later build uses the name:

```sh
iormeshctl services name -port 8443 'app2*' billing
iormeshctl services rules             # the naming rules, with their ids
iormeshctl services unname RULE-ID    # drop one
iormeshctl services                   # services, endpoints and counts
```

A client is named after its project's app, or, with no app,
after its process name (`java`). The names live in the service
database, `~/.local/share/iormesh/services.db`, which a build
creates; before the first build `iormeshctl services` answers
`no service database at … yet (a build creates it)`. Rebuild to
see a new name.

Reference: [architecture.md](../architecture.md) (the controller,
the service database), [output-contract.md](../output-contract.md)
(segments and the graph), [visualization.md](../visualization.md)
(the summary lines in full),
[troubleshooting.md](../troubleshooting.md#no-events-nothing-to-see).

---

← [5. Deploy and check status](05-deploy-status.md) · [Contents](README.md) · Next: [7. Read the mesh](07-read-the-mesh.md) →

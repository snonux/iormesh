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
from the host. It prints one line per host and project; run on
Rocky 9.9 (task 4k4), the home cut to `~`:

```text
$ iormeshctl collect
iom1: project shop: 1 new files in ~/.local/share/iormesh/traces/shop/iom1
iom2: project billing: 1 new files in ~/.local/share/iormesh/traces/billing/iom2
$ iormeshctl collect iom1
iom1: project shop: 0 new files in ~/.local/share/iormesh/traces/shop/iom1
```

Collect as often as you like while the sessions run; segments
left on a host count against its disk limit.

**If not:** `app1: project shop: no /opt/iormesh/traces/shop/ready
yet, nothing to collect` means no minute has finished yet, or the
session never started: wait a minute (`sleep 60`) and check
`status`
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
  shows as one fused edge. While sessions run, the tcp part may
  end in `, N pending`, with a `pending` line below: connections
  of the build's last minute whose far end may sit in a segment
  not collected yet. They are in no edge of `show edges` yet
  (`graph.json` marks them `unresolved`); the next `refresh` a
  minute later fuses them, or reports them `unresolved` when no
  session traces the far end. A `-to` in the past keeps its
  pending ones: widen the window.
- **`health`** — `lost` counts records dropped because a ring
  buffer was full; `clock bounds` are how far apart two hosts'
  clocks can be, proved by traffic between them.
- **`session`** — a session to look at: one with no recorded end
  (still running is fine) or an unusual end such as `disk_limit`.
- **`unresolved`** — a peer no tracer saw. Trace it
  ([chapter 10](10-expand.md)) or leave it.
- **`sequence`** — conversations are built from http events,
  which the Java agent records ([chapter 8](08-java-query.md)).
  Without `-agent` it reads `0 conversations` even with fused
  edges.

The two-host run of chapter 5 (task 4k4), its client restarted
once under the session ([chapter 5](05-deploy-status.md#start-what-you-want-to-trace)),
no agent yet:

```text
built      billing+shop: 2 hosts, 2 sessions, 4 segments, 2857 events, 2026-11-26 08:33:20 - 2026-11-26 08:35:23 UTC
merged     2 services, 1 edges (1 fused, 0 mixed, 0 unresolved); tcp 1 fused, 0 unresolved
health     0 lost, 0 skipped runs, 0 unattributed, 112 attach gap; clock bounds <= 1.078427ms
clock      iom1 ~ iom2: 1 windows, width 1.078427ms-1.078427ms
sequence   0 conversations, 0 steps (0 time-ordered, 0 unordered)
session    billing/iom2 312f2e27: no end recorded (running, or cut short)
session    shop/iom1 2ab80e31: no end recorded (running, or cut short)
```

The `attach gap` is the restarted JVM's start: a `-pattern`
session takes up a new process at its exec, and the records lost
before it did are counted, not hidden
([schema.md](../schema.md), `drop_reason`).

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

The host name is the one the client asked for (its DNS lookup,
or an HTTP request's host), not the registry's name for the
server. A client that dials an IP address leaves only the
address: on Rocky 9.9 (task 4k4) a shop that called
`http://10.20.0.141:8443` was not named by `'iom2*'`, only by
`'10.20.0.141'`. `iormeshctl services` shows what a rule can
match:

```text
$ iormeshctl services
SERVICE  ENDPOINT          CONNS  UNRESOLVED  TLS  CLIENTS
-        10.20.0.141:8443  1      0           ?    app:shop/java -jar */shop.jar*
$ iormeshctl services name -port 8443 '10.20.0.141' billing
rule 1: 10.20.0.141 port 8443 -> billing
```

A client is named after its project's app
(`app:shop/java -jar */shop.jar*`), or, with no app, after its
process name (`java`). The names live in the service
database, `~/.local/share/iormesh/services.db`, which a build
creates; before the first build `iormeshctl services` answers
`no service database at … yet (a build creates it)`. Rebuild to
see a new name.

**Checkpoint, before you read the mesh:** every server you traced
is named. After the rebuild, the TO column of `iormeshctl show
edges` holds names (`orders`, `billing`), not addresses such as
`127.0.0.1:18214@machine-id`; an address left there is a server
still to name, one rule per port.

**Checkpoint, every expected edge fused:** each edge between two
services you trace must read `fused` in `show edges`. A `mixed`
edge (some connections seen from one end only) or an
`unresolved` one is a fault to find, not a state to accept, and
refreshing until the count looks stable does not fix it. The
build already holds back
the connections of its last minute whose far end it may not have
collected yet (`pending`, above), so a one-sided connection older
than that most likely missed one end. What to do:

1. `iormeshctl status`: every session of both services is
   `running`. A session stopped, ended or never started on the
   far end's host misses its connections: deploy it again
   ([chapter 5](05-deploy-status.md)).
2. An edge whose `FIRST` is before the far end's session
   started (`deploy` printed `started …`): its one-sided
   connections were made while only one end was traced, and
   they stay in the service database, which keeps every
   build's connections. Let the sessions run a few
   minutes, `refresh`, and check that the new connections fuse
   (`CONNS` grows, `UNRESOLVED` does not). Then drop the early
   segments: `prune -older-than DUR` lists the segments that
   ended more than `DUR` ago, `-apply` deletes them and makes the
   database forget the connections seen only in them
   ([chapter 9](09-tracing-for-days.md#trim-the-store-prune));
   `-apply` is a human step. `refresh` once more.
3. An edge whose `UNRESOLVED` still grows has an end no session
   traces: the far end runs outside the pattern, or on a host
   not registered. Check the pattern against `ps -eo args` on
   that host ([chapter 4](04-register-bootstrap.md#choosing-the-target)).

An edge left `mixed` is never acceptable, not even as a harmless
leftover of a few early connections: take the steps above, the
prune included (an agent proposes it and waits for the yes), or
stop and report the edge if none of them explains it.

Reference: [architecture.md](../architecture.md) (the controller,
the service database), [output-contract.md](../output-contract.md)
(segments and the graph), [visualization.md](../visualization.md)
(the summary lines in full),
[troubleshooting.md](../troubleshooting.md#no-events-nothing-to-see).

---

← [5. Deploy and check status](05-deploy-status.md) · [Contents](README.md) · Next: [7. Read the mesh](07-read-the-mesh.md) →

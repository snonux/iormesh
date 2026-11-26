# Service database

Part of [M4 — controller](../m4.md), in [Implementation
guide](../../implementation.md).

`internal/servicedb` (task hb4), sqlite through a pure-Go driver
(`modernc.org/sqlite` — keeps the controller cgo-free; the
buildgate's darwin/arm64 `CGO_ENABLED=0` build proves it):

```
internal/servicedb/db.go       # Open: 0600 file, 0700 dir, migrations
internal/servicedb/schema.go   # migrations[i]: user_version i -> i+1
internal/servicedb/observe.go  # Observe(sources, federate.Result)
internal/servicedb/traffic.go  # per-conn window, http, dns from events
internal/servicedb/builder.go  # fused / unresolved / same-host pairs
internal/servicedb/store.go    # Import: validate, upsert, supersede
internal/servicedb/forget.go   # Forget: drop pruned sources (task gk4)
internal/servicedb/rules.go    # naming rules: glob + port -> service
internal/servicedb/query.go    # Endpoints (unnamed first)
internal/servicedb/edges.go    # Interactions: who talks to whom
internal/ctl/services.go       # iormeshctl services list|edges|name|rules|unname
```

```sql
CREATE TABLE conn (                -- one row per observed TCP connection
  host TEXT, conn_id TEXT,         -- the anchor end: client half if traced,
  PRIMARY KEY (host, conn_id),     --   else the server half (stable ids)
  role, state, reason, merged_id,  -- fused | unresolved + the merge's reason
  project, client_{host,project,app,comm},
  server_{host,conn,project,app,comm},
  ip, port, hostname, tls,         -- the endpoint as the client saw it
  first_seen, last_seen);          -- wall ns
CREATE TABLE http_request (        -- per conn and source (v2): method,
  ..., source, requests,           --   scheme, host, path; a count per
  first_seen, last_seen,           --   source, summed at read time
  FOREIGN KEY (host, conn_id) REFERENCES conn ON DELETE CASCADE);
CREATE TABLE conn_source (         -- v4 (task gk4): the sources that saw
  host, conn_id, source,           --   a conn, each with its window, so
  first_seen, last_seen);          --   DB.Forget can drop pruned segments
CREATE TABLE naming_rule (         -- human input: never touched by build
  id INTEGER PRIMARY KEY, host_glob TEXT, port INTEGER, -- 0: any
  service TEXT, UNIQUE (host_glob, port));
```

The build path is two calls the build (task ib4) makes after
`federate.Merge`: `Observe(ctx, sources, result)` reads the same
sources once more for what the merge drops — http and dns events
and each connection's last record — and adds the same-host pairs
the host's correlator fused (no halves, so not in the merge's
result); `DB.Import(ctx, snapshot)` folds that in, in one
transaction, after validating every observation (a corrupt one
rejects the whole snapshot).

- **Idempotent.** Rows are keyed by the anchor end's host-local
  conn id, which is the same on every rebuild, and every merged
  column is a MIN, a MAX or keep-if-unknown
  (`COALESCE(NULLIF(new, ''), old)`): importing the same mesh
  twice changes no byte.
- **Exact request counts, per source.** One count per
  connection could only keep the larger part when a pooled
  connection's requests span segments and the older ones were
  deleted. So the count is kept per source (`federate.Source`'s
  name, the unit a build reads; a parquet source should be one
  segment) and summed at read time: a deleted segment's count
  stays, a re-read one rewrites the same number. The client's
  view (its events carry the host) and the server's view count
  the same requests and are kept apart; per connection the
  reads take the larger view's sum, never both. The count is
  exact while one view's segments were all imported at some
  point, and a lower bound otherwise.
- **Fused wins.** A fused verdict never reverts to unresolved; a
  connection first seen from its server end alone (only that
  project built) is taken over by the fused row once both
  projects are built together — its window, TLS hint and
  requests move over — and a later server-only build does not
  add it back.
- **Hostname.** The client's HTTP host when that is a name;
  else the latest DNS lookup by the client's processes that
  returned the endpoint's address before the connection's first
  record on the client's own clock — a best guess, since one
  address can serve many names (a CDN, virtual hosting).
- **Opening.** The path is made absolute first (the driver
  would read `file:` as a URI and `:memory:` as no file);
  transactions are `BEGIN IMMEDIATE` and each migration step
  re-reads `user_version` inside its transaction, so two runs
  queue instead of racing. Reading verbs use `OpenExisting`
  and never create the database.
- **Names are resolved at read time** in Go (`path.Match`, the
  rule's glob against the endpoint's hostname and its IP,
  case-insensitive): a port-specific rule beats an any-port one,
  then the longer glob, then the older rule. A new rule applies
  retroactively without rewriting anything; a duplicate (glob,
  port) is refused rather than overwriting a human's decision.
- **Who talks to whom.** `Interactions` labels an endpoint by its
  service (else `hostname:port`, else `ip:port`) and a client by
  the service its own endpoints are all named, else its
  app id or process name — `iormeshctl services edges -from
  orders` answers "what does orders talk to".
- A half without a role (the merge's `no_role`) is stored but
  names no endpoint: its far end may be a client's ephemeral
  port.
- **Local addresses are per host** (task ai4). An endpoint is
  (hostname, ip, port), plus the anchor host when the IP is
  loopback, link-local or unspecified: such an address names a
  different machine on every host, so two hosts' 127.0.0.1:5432
  are two endpoints, and their unnamed labels end in `@host`.
  The scope is computed at read time (`EndpointHost`, a SQL
  function registered with the driver), so no column or
  migration was needed and older rows scope the same way.

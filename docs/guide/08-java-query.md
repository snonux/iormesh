# 8. Java code flow and query

Without the Java agent, iormesh still sees every file and socket
call of a JVM, but not which Java code made it. With the agent,
every step carries the stack of the thread behind it (chapter 7
shows them). This chapter turns the agent on for a project, then
goes from a step down to its raw events with `query`.

## Turn on the Java agent

The agent jar ships like the tracer: `mage agentBuild` puts it in
`dist/iormesh-agent.jar`, `mage sign` signs it
([chapter 3](03-release.md)), and `deploy` pushes it to every host
with a project that uses it, where the tracer installs it as
`/opt/iormesh/lib/iormesh-agent.jar`. A project uses it when its
registry entry carries `-spool` and `-agent` after HOST:

```sh
iormeshctl add -project shop -pattern 'java -jar */shop.jar*' app1 \
    -duration 8h \
    -spool /run/iormesh/spool/shop -agent /opt/iormesh/lib/iormesh-agent.jar
iormeshctl deploy app1
```

A deployed session may use only that jar and its own project's
spool, `/run/iormesh/spool/<project>`. `add` refuses a project
that is already registered on the host; to add the flags to an
existing one, `iormeshctl rm -project shop app1` (it stops and
collects that project) and add it again.

What `-agent` reaches:

- **JVMs running when the session starts** get the agent at once
  (live attach). Their I/O before that moment stays unattributed.
- **JVMs started later in the session** (a later `-pattern`
  match, a restarted `-listen-port` owner) get it by late attach,
  up to 30 s after their exec. Their start-up — class loading,
  config reads, the first connections — stays unattributed; the
  tracer says so on its log: `agent loaded into pid N; its I/O
  before now stays unattributed`.
- **To cover a JVM's start**, start it with
  `-javaagent:/opt/iormesh/lib/iormesh-agent.jar=spool=/run/iormesh/spool/shop`,
  for a systemd service through a drop-in that sets
  `JAVA_TOOL_OPTIONS`. That is a change to how the application
  starts, which iormesh never makes for you.

> **Human step.** A `-javaagent` drop-in changes how a production
> service starts and needs its restart. The service owner decides
> and applies it.

**Checkpoint:** after the next `refresh`, the steps of `show
sequence` carry `at class#method(File.java:line)` frames, and
`show` reports few `unattributed` events. A JVM that refused the
agent is named on the session's log (`no Java attribution for pid
…: …`) and traced on without stacks.

Reference: [java-attach.md](../java-attach.md) (the drop-in recipe
and its caveats, live and late attach, second sessions, checked
JDKs), [troubleshooting.md](../troubleshooting.md#java-agent)
(every agent message).

## From a step to its raw events: `query`

Every step's `ref` is one raw event away. From chapter 7's
checkout, billing's rename of its invoice (step 36):

```text
$ iormeshctl query -ref iormesh-it/local/host1-79d2d3d5/4679
{"schema_version":1,"project":"iormesh-it","seq":4679,"boot_ns":1353285566928846,"wall_ns":1795512264031679218,"host_id":"machine-id","pid":2483913,"pid_incarnation":1353273770000000,"tid":2484384,"comm":"java","event":"rename","fd":-1,"fd_gen":0,"ret":0,"latency_ns":30640,"bytes":0,"args":{"path":"/tmp/TestMicroservices1125629951/002/billing.d/invoice-42.json.tmp","new_path":"/tmp/TestMicroservices1125629951/002/billing.d/invoice-42.json"},"attribution":"java","java":{"thread":"HTTP-Dispatcher","stack":[274,275,276,277,278,265,266,267,268,269]}}
```

One NDJSON line per event, in the trace schema. The `stack` holds
method ids of the session's methods table, not frames; `show
sequence` and the site print the frames. `query -event http`
prints every http event the same way, both ends of each hop:
[query-http.ndjson](../examples/query-http.ndjson).

`query` reads the store, not a build, so it works on whatever
`collect` fetched. Its filters AND:

| Filter | Selects |
|---|---|
| `-ref REF` | one event, by a step's ref |
| `-event KIND` | one event kind, exactly (`openat`, `connect`, `http`, …) |
| `-host GLOB`, `-app GLOB`, `-pid PID`, `-thread GLOB` | where: store host or FQDN, app pattern or comm, process, Java thread name or kernel tid |
| `-file GLOB` | events naming a path, and the fds opened from it |
| `-method GLOB` | a Java frame, `class#method` or `class#method(file:line)` |
| `-conn ID` (`-follow`) | one merged connection (and its threads' other events while they used it) |
| `-errno NAME` | calls that failed with it (`ENOENT` or `2`) |
| `-otel-trace ID` | events inside an OpenTelemetry trace's spans |
| `-from T`, `-to T` | a window; `-to` is exclusive |

`T` is RFC 3339, `YYYY-MM-DDTHH:MM:SS` (UTC), or a bare
`HH:MM:SS` (UTC) while the store spans one UTC day; query echoes
the resolved window on stderr. A folded run's `run` line in `show
sequence` is such a query, ready to paste.

The output stops at `-max-bytes` (1 MiB by default); a cut slice
ends in a truncation record. From the test golden
`internal/ctl/testdata/query_capped.golden`:

```text
{"schema_version":1,"event":"truncated","args":{"emitted":3,"remaining":19}}
```

Narrow the filters rather than raising the cap.

## The rollup

`iormeshctl show report` prints the summary, then the per-run
rollup the build wrote to `report.json`: per process its files,
connections, threads with their requests in order, and the top
Java methods. It is the first thing to read (or to hand to an
LLM, [chapter 13](13-agents.md)) before slicing with `query`.

Reference: [output-contract.md](../output-contract.md) (why the
trace is shaped for reading), [schema.md](../schema.md) (every
field and event kind, the rollup),
[visualization.md](../visualization.md) (how steps, stacks and
refs are built).

---

← [7. Read the mesh](07-read-the-mesh.md) · [Contents](README.md) · Next: [9. Tracing for days](09-tracing-for-days.md) →

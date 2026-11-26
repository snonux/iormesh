# M4 scenarios

Part of [M4 — controller](../m4.md), in [Implementation
guide](../../implementation.md).

## The microservices scenario

`microservices` (task jb4) landed ahead of the multi-host work,
on loopback. `Microservices` parses one role's options. `Service`
holds the topology: the roles, the calls each serves and the
hops it makes. `ServiceNode` runs one service from boot to
retirement. `DataStore#save` writes a record the
create-tmp-then-rename way, and `ServiceConfig` provisions and
loads the config. A server's request work (orders' downstream
calls, every server's record) runs through `HttpServe.Work`
on the HttpServer's dispatcher thread, inside the request, as a
synchronous REST service does it. `Scenario.roles()` lets the
scenario name its four roles instead of client/server/solo.

The address options are built (task 1f4). A server binds
`--bind`, and its certificate (`SelfSignedCert.generate(ip)`)
names that address as its only SAN. A caller dials each callee
at `--<callee>-host`. Both are IPv4 literals parsed by
`TcpHosts.ipv4`, defaulting to 127.0.0.1. `ServiceNode` takes
the callees as `InetSocketAddress`es. A caller still pins the
certificate from its own `--dir`, and the deployment puts it
there: the rig shares one `--dir`, and `internal/acceptance`
copies the public file between the hosts. On the Go side,
`Host.Spool` with `Harness.StartWorkloadOn` gives each emulated
host its own agent spool, which only its tracer drains.
`mergeHosts` merges any number of host traces and keeps each
host's own event order. `microservices_hosts_test.go` spreads
the services over three hosts against `expect-spread.json`.
Testing notes: [testing.md](../../testing/integration/m4.md#m4-microservices-scenario-task-jb4).


## The two-host and NAT'ed pairs

`tcp-two-host` and `tcp-nat` (task kb4) are `TcpHosts`, M0's
client and server with `--bind`/`--host` IPv4 literals. The rig
the microservices run will reuse is built:
`integrationtests/multihost.go` adds `Host`,
`Harness.StartWorkloadIn` (a workload in a host's network
namespace) and `Harness.TraceHosts` (one tracer per host, each
with its own machine id, all attached before the workloads are
released across hosts in order); `multihost_test.go` builds the
three namespaces and the docker-style DNAT and merges the host
traces with `federate.Merge`, renaming each event's conn id by
`Result.Renames` before the merged golden check.
Testing notes: [testing.md](../../testing/integration/m4.md#m4-two-host-and-nated-pairs-task-kb4).

**Done when**: milestones.md's M4 acceptance holds, driven by
`integrationtests/multihost_test.go` on two network namespaces
plus one run on two real hosts. That run is `mage m4Acceptance
HOST1 HOST2` (task lb4): `internal/acceptance` holds the
sequence, one check per criterion and a preflight refusing an
unbootstrapped, busy or third host; `fake_test.go` drives it
against fake hosts and a fake iormeshctl. Runbook and status:
[testing.md](../../testing/m4-acceptance.md#m4-acceptance-on-two-real-hosts-task-lb4).

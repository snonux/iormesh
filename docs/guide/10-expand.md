# 10. Grow the mesh

An unresolved edge is a connection whose far end no tracer saw.
`expand` turns those far ends into candidate hosts, checks them
against an allowlist you wrote, and — only when told — registers
them and deploys a tracer bound to the process listening on the
observed port. The tracers never do this themselves: every hop is
your SSH key, from the laptop.

## Write the allowlist

The allowlist lives in the registry, beside the hosts. Without an
`expand:` section nothing is allowed.

```yaml
expand:
  allow:
    cidrs: [10.0.0.0/8]       # canonical, no host bits
    hosts: ["198.51.100.*"]    # address-text globs; prefer cidrs
  max_nodes: 16               # registered hosts included
```

Keep load balancers, VIPs, NAT fronts and container bridges (such
as `172.17.0.0/16`) out of it: expand proposes a peer by the
address the client reached, never by a name it looked up, so such
a front would be proposed as if it were the server.

> **Human step.** The allowlist decides which hosts your key may
> push a root tracer to. The human writes it.

## List the candidates

After a `refresh`:

```sh
iormeshctl expand                 # lists; contacts nothing, deploys nothing
iormeshctl expand -json           # the same plan as JSON
```

Each unresolved peer is listed with the traced app that reached
it and either as a candidate or with the reason it is skipped:
`not_allowed` (no allowlist rule), `hop_limit` (further than
`-hops` rounds from the hosts you added by hand; 1 by default),
`node_cap` (`max_nodes` reached), `enrolled` (already registered
or traced), `nat` (a fused connection proves it is NAT in front
of a traced server), `transport` (the controller's own SSH or
rsync), `local`, `inbound`, `ipv6` and a few more. Nothing is
contacted.

## Apply one round

> **Human step.** `-apply` registers new hosts and deploys to
> them. Show the listed plan first; the human says yes.

```sh
iormeshctl expand -apply          # -dist and -key as deploy's
```

Each candidate becomes a registry host written as its address,
under the first project whose connections revealed it, bound to
its listening port; then deploy runs as in
[chapter 5](05-deploy-status.md). The new entry carries no tracer
flags of its own, so its session runs for the default 4h, not
the revealing project's `-duration`; add flags to its registry
entry and deploy again to change that. SSH reaches it by the
address, so `~/.ssh/known_hosts` needs the host's key under that
address too (one under its name in `~/.ssh/config` with a
`HostName` of that address is the same entry).

Run on Rocky 9.9 (task 4k4): `shop` traced on `iom1` only, its
peer `iom2` (bootstrapped, untraced) allowed as `10.20.0.141/32`:

```text
$ iormeshctl expand -apply
expand: 1 to deploy, 0 skipped (hop limit 1, 1 of 4 nodes registered)

deploy
  10.20.0.141:8443      hop 1  1 conn  cidr 10.20.0.141/32
      via iom1 app:shop/java -jar */shop.jar* (1 conn)
enrolled 10.20.0.141 hop 1 project shop listen-port 8443 (~/.config/iormesh/servers.yaml)
verified dist/iormesh-linux-amd64: iormesh-linux-amd64 0.1.0
10.20.0.141: iormesh: iormesh-linux-amd64 0.1.0 is already installed
10.20.0.141: iormesh: started iormesh-shop.service: session 032391bb on iom2, pid 11377, tracing port 8443 owners, pid [11057] until 2026-11-26T12:50:10Z
…
$ iormeshctl list
KIND  TARGET       PROJECT  TRACES                         FLAGS         NOTE
host  iom1         shop     pattern java -jar */shop.jar*  -duration 1h  -
host  10.20.0.141  shop     listen-port 8443               -             hop 1
```

After the next refresh the edge read `mixed` (2 connections, 1
unresolved): the keep-alive connection that revealed the peer
predates its session and stays one-sided
([chapter 5](05-deploy-status.md#start-what-you-want-to-trace));
the client's next connection fused. `expand` then lists the peer
as `enrolled`. Each new host still needs
bootstrapping first ([chapter 4](04-register-bootstrap.md)) unless
it already has the tracer: deploy only upgrades an installed one.
A damaged segment in the store refuses `-apply`.

One `-apply` is one round. Let traffic flow, `refresh`, and
`expand` again for the next ring; raise `-hops` to go further. A
peer's tracer starts after the connection that revealed it, so
earlier conversations stay one-sided: those edges stay
unresolved, never dropped and never invented.

Reference: [milestones.md](../milestones.md#m6--mesh-expansion-follow-the-edges-from-the-controller)
(the rules and their acceptance), [security.md](../security.md)
(the registry as the deploy boundary),
[implementation.md](../implementation.md) (every skip reason).

---

← [9. Tracing for days](09-tracing-for-days.md) · [Contents](README.md) · Next: [11. Kubernetes](11-kubernetes.md) →

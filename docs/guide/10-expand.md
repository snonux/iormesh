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
[chapter 5](05-deploy-status.md). Each new host still needs
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

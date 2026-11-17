# Security & scope

- Privileged (root) for eBPF; read-only tracing only — no tampering.
  - Remote deployment needs an explicit allowlist of hosts/destinations
    and a signed tracer binary.
- Self-replication guardrails: hop limit, max concurrent nodes,
  domain egress rules. This is the part that makes it a *mesh* — and
  the part that must be tightly controlled.
- Metadata-only tracing: no file or socket payload contents are
  recorded — paths, sizes, offsets, endpoints, stacks. The one
  deliberate exception: HTTP(S) request metadata (method, URI,
  headers), captured at the Java API boundary — never sniffed
  off the wire — with sensitive headers and query params
  redacted by policy. (Argv is metadata; env capture is a policy
  decision, see [PLAN.md](../PLAN.md).)
- Kubernetes: eBPF privileges are node-kernel-wide — a sidecar
  with CAP_BPF can in principle observe any process on the node.
  iormesh scopes itself by config to the pod it runs in, is added
  only by the deployment owner's explicit patch, and its
  container image is signed like the tracer binary.
- No management sockets: the tracer listens on nothing — control
  flows only through SSH (or kubectl exec), i.e. the operator's
  key.
- The test workload keeps guardrails honest too: propagation rules
  are rehearsed in a sandbox before they exist in the wild.

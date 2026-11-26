# Security & scope

The decisions it cites are in [decisions.md](decisions.md).

## Parts

This file is the index: each part below is its own file. Read the summaries,
then open only the part you need.

- [Access and trust boundaries](security/access.md) — read-only tracing, SSH,
  the registry, hostile servers and trace text, host keys, expansion.
- [Signed tracer binary and the sudoers rule](security/sudoers.md) — bootstrap,
  signing, upgrade, deploy/status/ensure, what the one rule grants and refuses.
- [The Java agent](security/java-agent.md) — the untrusted spool, live and late
  attach.
- [Trace data](security/trace-data.md) — bounded sessions, metadata only,
  redaction, the trace directory, sensitive traces, prune.
- [Kubernetes](security/kubernetes.md) — the tracer pod's privileges, SELinux,
  scoping, staging, kubectl, the image, collection.

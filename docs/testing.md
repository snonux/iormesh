# Testing strategy

The test workload comes *first*, not last:

- Build the workload app before (or alongside) the tracer, so every
  milestone lands against a known target.
- Each scenario declares the exact I/O and method-flow it produces;
  validation = diff the collected graph against that expectation.
- The app has client and server modes — and a microservices flavor
  where several service roles talk REST over plain HTTP and TLS —
  so single-hop and multi-hop mesh propagation can be rehearsed on
  loopback, between two test VMs/containers or across test hosts,
  without going near anything live.
- Only once a milestone passes against the workload is it considered
  ready for a live process.
- Expected graphs are declared in the same trace schema as real
  output, so validation is a plain machine diff — and the goldens
  double as few-shot examples for LLM introspection.
- Deterministic SVG gives M0 visual goldens; the controller flow
  (add → deploy → collect → rm) is rehearsed against M0 on two
  test hosts, and its pull-merged mesh must equal M3's propagated
  result; the k8s mode rehearses the same scenarios as pods in
  kind.

## Integration tests follow ior

The end-to-end suite lifts ior's `integrationtests/` pattern
(in `../ior`): a dedicated package with a harness that runs the
tracer against the known workload, two expectation layers
(aggregated events vs exact per-event rows), one test file per
trace family, kernel-support probes with explained skips,
loss-honest re-runs, unexercised-skip markers, and pass gates
that do not trust exit codes. See PLAN.md, "Integration tests —
follow ior" for the full map.

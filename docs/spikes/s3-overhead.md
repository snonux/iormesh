# S3 — overhead and volume budget

Result of spike S3 (docs/milestones.md, "Spikes"; decision #25). It measures
what the M1 tracer costs on the per-syscall worst case and turns
the numbers into the budget M2 is accepted against. Every figure
below is computed from `spikes/s3/results/samples.txt`, written
by `spikes/s3/measure.sh`.

## Parts

This file is the index: each part below is its own file. Read the summaries,
then open only the part you need.

- [Setup, numbers and conclusions](s3-overhead/baseline.md) — the original S3
  measurement.
  - [Setup](s3-overhead/baseline.md#setup)
  - [Numbers (medians of 3)](s3-overhead/baseline.md#numbers-medians-of-3)
  - [Conclusions](s3-overhead/baseline.md#conclusions)
- [Consumer CPU, aggregation, native stacks,
  M2](s3-overhead/early-follow-ups.md) — wc4, vc4, ua4 and the M2 acceptance
  measurement.
  - [Follow-up (wc4): the consumer's per-event
    CPU](s3-overhead/early-follow-ups.md#follow-up-wc4-the-consumers-per-event-cpu)
  - [Follow-up (vc4): in-kernel
    aggregation](s3-overhead/early-follow-ups.md#follow-up-vc4-in-kernel-aggregation)
    - [Follow-up (vc4,
      re-run)](s3-overhead/early-follow-ups.md#follow-up-vc4-re-run)
  - [Follow-up (ua4): the cost of native
    stacks](s3-overhead/early-follow-ups.md#follow-up-ua4-the-cost-of-native-stacks)
  - [M2 acceptance measurement
    (2026-11-19)](s3-overhead/early-follow-ups.md#m2-acceptance-measurement-2026-11-19)
- [Paired CPU-time method; Java and HTTP costs](s3-overhead/java-http.md) —
  md4, 2b4, 3b4, td4.
  - [Follow-up (md4): the timing items by the paired CPU-time
    method](s3-overhead/java-http.md#follow-up-md4-the-timing-items-by-the-paired-cpu-time-method)
  - [Follow-up (2b4): the Java join's
    cost](s3-overhead/java-http.md#follow-up-2b4-the-java-joins-cost)
  - [Follow-up (3b4): the HTTP events'
    share](s3-overhead/java-http.md#follow-up-3b4-the-http-events-share)
  - [Follow-up (td4): the Java mark's
    cost](s3-overhead/java-http.md#follow-up-td4-the-java-marks-cost)
- [Bystander and consumer cost](s3-overhead/consumer-bystander.md) — ae4, ud4,
  xe4.
  - [Follow-up (ae4): the bystander after 8e4's `raw_syscalls`
    dispatch](s3-overhead/consumer-bystander.md#follow-up-ae4-the-bystander-after-8e4s-raw_syscalls-dispatch)
  - [Follow-up (ud4): cutting the consumer's per-event
    cost](s3-overhead/consumer-bystander.md#follow-up-ud4-cutting-the-consumers-per-event-cost)
  - [Follow-up (xe4): the cost of
    `args.tcp_seq`](s3-overhead/consumer-bystander.md#follow-up-xe4-the-cost-of-argstcp_seq)
- [Follow-up (ee4): tracer CPU on M4's segment
  writer](s3-overhead/segment-writer.md) — ee4, its independent readers and the
  owner decision.
  - [Follow-up (ee4): independent
    readers](s3-overhead/segment-writer.md#follow-up-ee4-independent-readers)
  - [Follow-up (ee4): owner
    decision](s3-overhead/segment-writer.md#follow-up-ee4-owner-decision)
- [PID namespaces](s3-overhead/namespaces.md) — 4f4 and 9f4.
  - [Follow-up (4f4): PID-namespace-aware
    targeting](s3-overhead/namespaces.md#follow-up-4f4-pid-namespace-aware-targeting)
  - [Follow-up (9f4): cutting the namespace bystander's
    cost](s3-overhead/namespaces.md#follow-up-9f4-cutting-the-namespace-bystanders-cost)
- [Rocky 9.9 re-measure](s3-overhead/el9.md) — ak4 with the msghdr handlers,
  9m4 on el9's bystander cost.
  - [Re-measure on Rocky 9.9 with the msghdr handlers (ak4,
    2026-11-26)](s3-overhead/el9.md#re-measure-on-rocky-99-with-the-msghdr-handlers-ak4-2026-11-26)
    - [The gated items (`syscall-burst`, `-aggregate=false` for the CPU
      items)](s3-overhead/el9.md#the-gated-items-syscall-burst--aggregatefalse-for-the-cpu-items)
    - [The new loads (5 rounds
      each)](s3-overhead/el9.md#the-new-loads-5-rounds-each)
  - [Follow-up (9m4, 2026-11-26): where el9's bystander cost
    goes](s3-overhead/el9.md#follow-up-9m4-2026-11-26-where-el9s-bystander-cost-goes)

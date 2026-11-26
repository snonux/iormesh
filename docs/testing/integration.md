# Integration tests follow ior

Part of [Testing strategy](../testing.md).

iormesh does not invent its end-to-end test machinery: the
suite lifts ior's `integrationtests/` package (in `../ior`, its
README first) with the workload swapped for M0's. Before
changing the suite, re-read ior; this section is the map, not
the replacement:

- **Suite layout**: a dedicated `integrationtests/` package and
  `mage integrationTest` (plus a serial variant) that builds
  the tracer, the M0 workload and the compiled test binary,
  then runs it as root — skipping with an explanation when
  root or BPF is unavailable. The test binary is compiled to
  `bin/` (ior uses the repo root; decision #30) so its absolute
  path matches a scoped sudoers rule.
- **Harness** (ior's `harness.go`): start the tracer against
  a known M0 workload process — or the whole microservices
  topology — collect the trace output (NDJSON live, parquet
  segments), and expose parsed rows for assertions; ior's
  readiness wait, timeouts and BPF-object override env apply
  as-is.
- **Two expectation layers** (ior's `expectations.go`):
  aggregated event assertions (path contains, event, comm,
  min count, flag bits) over the recording, and exact
  per-event fields (fd, fd_gen, ret, err, bytes, latency)
  over the parquet rows — aggregate where exactness doesn't
  matter, row-exact where it does.
- **Per-family test files**: one `*_test.go` per trace family,
  each running its M0 scenario and asserting the declared
  events — file ops (data + meta: rename, symlink, touch,
  xattr, chmod/chown…), sockets (IPv4/IPv6, TCP/UDP
  lifecycle), local IPC, signals, process/thread/fd
  lifecycle, and with M3: Java stacks,
  class loads, HTTP events (plain + TLS — including the
  redaction expectations: the planted Authorization header,
  query secret and argv secret must be absent), OTEL ids.
- **The golden check is the integration-level assertion**:
  the normalized trace, cut to the scenario's marker bracket,
  must contain everything the scenario declares and nothing
  on its must-not-appear list ("What a golden check asserts"
  above) — subset semantics, the same spirit as ior's min-count
  and row-presence assertions. The per-family expectations are
  the fine-grained complement, not a replacement.
- **Multi-host tests**: the microservices topology across two
  test hosts (or containers in distinct network namespaces)
  drives deploy → collect → merge (and expand, M6) and asserts
  the fused cross-host edges and the sequence view's thread
  causality.
- **Emulated hosts share a kernel — compensate**: tracers in
  network namespaces on one machine share one clock, so
  reconciliation would never be exercised. A test-only flag
  injects a clock offset per tracer; the merge must recover
  it within its recorded bound. NAT is emulated the same way
  (a DNAT hop between namespaces). Built (task ye4): the knob
  is the environment variable `IORMESH_TEST_CLOCK_SKEW` on
  `run` — not a flag, so no usage text shows it and `start`
  cannot forward it; refused through the sudoers rule and by
  `start` — and shifts `wall_ns` only, the clock the fusion
  window and the bounds use; `boot_ns`, shared by the emulated
  hosts, is the truth the tests order against. Skews of
  −0.25 s and +1.25 s fuse and are recovered inside two-sided
  bounds ≈250–800 µs wide, with no cross-host order
  contradicting the truth; +3 min fuses nothing (`no_match`,
  no bound) — see "M4 two-host and NAT'ed pairs" below.
- **Kernel-support probes** (ior's `kernel_support_test.go`):
  probe for the newer syscalls/tracepoints a scenario needs
  and skip with the reason — never infer support from kernel
  version.
- **Loss honesty** (ior's `kernelloss.go` + `unexercised.go`):
  a run missing events that kernel-side loss could explain
  is re-run; a test none of whose runs exercised its case
  skips with a marker instead of passing — a silent green is
  not a green.
- **Gates, not exit codes** (ior's `gatecmd`): the runner
  verifies the required tests actually reported passed and
  prints a skip summary — exit 0 alone is not believed.
- **Parallelism**: `INTEGRATION_PARALLEL`-style knob with
  `-test.failfast` by default, serial mode for debugging.

## Parts

Each part is its own file; open only the one you need.

- [The suite today](integration/suite.md) — what the integration suite
  covers; M2 acceptance.
- [M3 scenarios and live attach](integration/m3.md) — M3 scenarios
  ahead of the agent; live attach (ge4).
- [M4 on the rig](integration/m4.md) — the microservices scenario
  (jb4), two-host and NAT'ed pairs (kb4).
- [M5 and M6 on the rig](integration/m5-m6.md) — M6 (tb4) and M5 (rb4)
  on the rig.

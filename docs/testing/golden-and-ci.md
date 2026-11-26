# Golden checks, race gate and CI

Part of [Testing strategy](../testing.md).

## What a golden check asserts

A JVM issues thousands of syscalls nobody declared — startup,
class loading, JIT and GC threads — and PIDs, TIDs, ports and
inodes differ on every run. So validation is subset, not
equality:

1. **Cut to the marker bracket**: every scenario emits a begin
   and an end marker; nothing outside them is asserted on. A
   child the scenario forks inside its bracket shares it (the
   graph's `forked` edge says whose child it is).
2. **Normalize** what is left: run-specific values become
   symbolic ids, in order of first appearance inside the
   bracket, so startup noise never shifts them.
3. **Declared ⊆ collected**: every declared event and edge is
   present, in the declared per-thread order.
4. **Must-not-appear**: the scenario's forbidden list holds —
   e.g. the planted `Authorization` header, query secret and argv
   secret never show up.

Expectations are declared in the same trace schema as real
output, so they double as few-shot examples for LLM
introspection.


## Beyond single-host

- Deterministic SVG gives the scenarios visual goldens (rendered
  from normalized graphs).
- The controller flow (add → deploy → refresh → rm) is rehearsed
  on two test hosts; its merged mesh must equal the single-host
  result for single-host scenarios.
- Mesh expansion is rehearsed on the microservices topology with
  only the gateway enrolled; the expanded mesh must equal the one
  obtained by enrolling every host by hand.
- Hosts emulated as network namespaces share one kernel clock, so
  a test-only knob (`IORMESH_TEST_CLOCK_SKEW`, below) injects a
  clock offset per tracer, and the merge must recover it; NAT is
  emulated with a DNAT hop.
- The k8s mode rehearses M0's client/server and ingress
  scenarios as pods in kind (milestones.md M7); the microservices
  scenario as pods is planned (task uh4).
- The LLM claim is tested: a fixed question set over workload
  traces, with answers known from the scenario declarations.
  It passed with real models on 2026-11-26 ("First runs with
  real models", below), which accepted M5; it is to be rerun on
  every schema change.


## Race detector gate (task ij4)

`mage testRace` runs the Go suite once more under `-race`
(`go test -race -count=1 -failfast -timeout=15m`). It belongs
to the gate beside `mage test`; `mage test` itself stays
without `-race`, since the detector multiplies the cost of the
two slowest packages many times over. The package set is
every package `go list ./...` names minus a deny-list,
`gatecmd.RaceExcluded`, so a new package is race-checked from
its first commit; `internal/buildgate` pins the argv and the
list.

Measured on 2026-11-22 (`go test -race -p 2 ./internal/...`,
GOMAXPROCS=4, nice 15, a loaded host):

| package | under `-race` | in the race gate |
|---|---|---|
| `internal/generate` | 2099 s | no: its tests compile and run C harness programs, and its Go code starts no goroutine |
| `internal/ctl` | 947 s | no: its code starts no goroutine and no test calls `t.Parallel` (the tests' one goroutine is `runBounded`'s watchdog, synchronised by a channel); what it drives (segment, federate, k8s) is race-checked in those packages |
| every other package | under 60 s each | yes, among them all that start goroutines: acceptance, bpf, cli, stacks |

So the gate costs a few minutes, not the 50 the whole module
would. The 15-minute timeout is per test binary and stops a
hung test long before `mage test`'s 90. A package that turns
out slow under `-race` goes into `RaceExcluded` with its
measured cost and a row here. `IORMESH_RACE_PKGS="./internal/a
./internal/b"` replaces the set for a run on one or two
packages while working on them.

The two failures the first run found are fixed: the acceptance
runner's log writer is locked (background workload roles log
while the run does), and `internal/segment`'s
`TestReadHostileFile` skips under `-race` only (a `race`
build-tagged constant): there parquet-go's `unsafe.Slice(nil,
n)` on a flipped footer is a fatal runtime throw, not the
panic the reader recovers, so it would kill the test binary.
It still runs in `mage test`. The root suites
(`mage bpfRootTest`, `mage integrationTest`) are not run under
`-race`.


## CI

CI (GitHub Actions, task ck4) runs the gates that need neither
root nor a particular kernel: `mage fmtCheck`, `vet`, `lint`,
`test`, `testRace`, `workloadTest` and `agentReproTest` (two
clean agent jar builds must match, task bm4), plus the generate diff
check, on an Ubuntu runner ([build.md](../build.md), "CI", has the
steps). What stays manual is everything that needs root, el9 or
a cluster: `mage bpfRootTest` and `mage integrationTest` (run as
root on Rocky 9.9, the main target, and on the dev host), the
el9 runbook runs below, and the kind and existing-cluster
acceptance (`mage k8sAcceptance`). A green CI run therefore says
nothing about the BPF object loading on el9's 5.14 kernel.

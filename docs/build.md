# Build & code generation

iormesh follows ior's BPF/C generation pattern wholesale, so
nothing below gets invented twice. Companion to
[PLAN.md](../PLAN.md) ("BPF/C code generation — follow ior" and
"Container builds").

- Tracepoint discovery from the build host's kernel → generated C
  enter/exit handlers, generated Go tracepoint tables and
  C-derived Go types — all from a unit-tested `internal/generate`
  package, driven by Mage targets.
- Generated artifacts are committed, with a committed result file as
  the diff gate pinning the kernel tracepoint set: regenerating on
  a different kernel shows up as a diff and stops the build.
- `vmlinux.h` from BTF, clang CO-RE compile, the `.bpf.o` embedded
  into the static binary via `go:embed` (env override to load an
  external object for testing).
- Container builds with Docker **or Podman**: the container pins
  the build environment; the static binary is
  copyable to any BTF host, and tracepoints missing there are
  skipped at runtime. The tracer builds for linux/amd64 + arm64
  from its container; the cgo-free controller cross-compiles
  darwin/linux for both arches from the same pipeline.

Where ior already solved a problem — fd→path resolution in BPF,
syscall enter/exit pairing and restart edge cases, thread/TID
handling, ring buffer consumption, safe libbpf link teardown —
iormesh reuses that approach and code rather than re-deriving it.
Every copied file records the ior commit it came from, so later
ior fixes can be ported deliberately.

Reuse has an edge. ior reads no socket peer addresses, follows no
child processes, and captures no stacks — so 4-tuples (from the
socket's state transitions), process-tree following with
exec-time pattern matching, stack capture and uprobes on the
JDK's native I/O functions are new work in iormesh. See PLAN.md,
"What ior does not give us".

## Building and testing

The tracer build is ior's, lifted in M1 (provenance headers name
the ior commit). Prerequisites, as for ior: a libbpfgo checkout at
`../libbpfgo` (or `$LIBBPFGO`) pinned to `v0.9.2-libbpf-1.5.1`
and built with `make -C ../libbpfgo libbpfgo-static`; clang;
bpftool; and non-interactive `sudo` for the two root reads
(`/sys/kernel/tracing` formats, `/sys/kernel/btf/vmlinux`).

```bash
mage generate      # tracepoint formats -> C handlers, Go tables, Go types
mage bpfBuild      # vmlinux.h (first run) + clang CO-RE -> iormesh.bpf.o
mage build         # bpfBuild + go build ./... + the tracer binary ./iormesh
mage test          # go test ./... (the generator's C harness tests too)
mage vet           # go vet ./... with the cgo environment
mage lint          # golangci-lint: errcheck + staticcheck SA
mage fmt / fmtCheck
mage clean         # keeps vmlinux.h (it needs root to recreate)
mage workloadTest  # the Java workload, standalone (M0)
mage bpfRootTest   # BPF loader + TCP tuple tests as root (sudo -n)
mage integrationTest        # tracer + workload end to end, as root
mage integrationTestSerial  # the same, one test at a time
mage agentBuild    # the Java agent -> ./iormesh-agent.jar (M3)
mage agentTest     # its unit tests + -javaagent smoke runs
```

The Java agent (`agent/`) is built with the JDK alone, like the
workload, compiled for 17 (decision #29). Its one dependency, ASM,
is vendored as the released jar in `agent/third_party/asm` with
its license and provenance; `mage agentBuild` checks the jar's
pinned SHA-256 and relocates its classes to
`iormesh.agent.shaded.asm` (`agent/tools/Relocate.java`), so
neither build nor tests touch the network. `mage agentTest` runs
with the `java` on `PATH` and again with each JDK listed in
`IORMESH_AGENT_JDKS` (colon-separated `JAVA_HOME`s), which is how
the `jdk17` hook table gets checked against a JDK 17.

Run Go commands through Mage: bare `go test ./...` or
`go vet ./...` cannot build `internal`, which imports libbpfgo,
because only Mage sets `CGO_CFLAGS`/`CGO_LDFLAGS`.

- **Generation host**: the committed artifacts were generated on
  Linux 7.2 and match ior's byte for byte but for iormesh's
  additions (the socket hooks of connect/accept, step 4's
  `tcp_state_event`). On a kernel with
  another tracepoint set `mage generate` stops at the diff of
  `internal/c/generated_tracepoints_result.txt` without touching
  the tree; `IORMESH_FORCE_GENERATE=1` adopts the new set (for
  throwaway container builds — do not commit an older kernel's
  deletions).
- **vmlinux.h** is dumped from the build host's BTF and
  gitignored, as in ior; CO-RE relocations make the object
  portable anyway.
- **Attach set**: handlers are generated for every syscall, but
  without a `-trace-*` selector or `-tps` regex only M1's set
  attaches
  (`tracepoints.DefaultSyscalls`). Widening it is a code change,
  not a regeneration.
- **External object**: `IORMESH_BPF_OBJECT=path.bpf.o` loads that
  object instead of the embedded one.
- **`//nolint` is banned** (`internal/buildgate`): a deliberate
  discard is an explicit `_ =`; an exemption goes into
  `.golangci.yml` with its reasoning.

## Tech choices

- Go + libbpfgo + eBPF (CO-RE) — the same stack as ior, adopted
  along with its code-generation pattern (see above).
- Java agent: bytecode instrumentation at the JDK's I/O
  boundaries only, stacks captured there; joined to syscalls
  by thread id through eBPF uprobes on the JDK's native I/O
  functions (settled by spike S1 —
  [result](spikes/s1-java-join.md)). Platform threads only in
  v1.
- Controller (`iormeshctl`): pure Go, no cgo — cross-compiled
  for Linux and macOS; tracer: linux/amd64 + linux/arm64 with
  the embedded CO-RE object.
- Storage: in-memory ring buffer → parquet trace files on
  servers (minute-rotated segments, parquet-internal zstd;
  ior's parquet pattern); graph + summary exports, rebuilt on
  the controller at every build — no server-side database.

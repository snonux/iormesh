# Tech choices

Part of [Build & code generation](../build.md).

- Go + libbpfgo + eBPF (CO-RE) — the same stack as ior, adopted
  along with its code-generation pattern (see above).
- Java agent: bytecode instrumentation at the JDK's I/O
  boundaries only, stacks captured there; joined to syscalls
  by thread id through eBPF uprobes on the JDK's native I/O
  functions (settled by spike S1 —
  [result](../spikes/s1-java-join.md)). Platform threads only in
  v1.
- Controller (`iormeshctl`): pure Go, no cgo — cross-compiled
  for Linux and macOS; tracer: linux/amd64 + linux/arm64 with
  the embedded CO-RE object.
- Storage: in-memory ring buffer → parquet trace files on
  servers (minute-rotated segments, parquet-internal zstd;
  ior's parquet pattern); graph + summary exports, rebuilt on
  the controller at every build — no server-side database.

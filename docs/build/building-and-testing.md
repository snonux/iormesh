# Building and testing

Part of [Build & code generation](../build.md).

The tracer build is ior's, lifted in M1 (provenance headers name
the ior commit). Go: 1.26 (go.mod's floor, what CI uses) or 1.27;
go.mod has no `toolchain` line, and code must be gofmt-clean and
pass the gates under both (decision #33). Prerequisites, as for
ior: a libbpfgo checkout at `../libbpfgo` (or `$LIBBPFGO`)
pinned to `v0.9.2-libbpf-1.5.1` and built with `make -C ../libbpfgo libbpfgo-static`; clang;
bpftool; mage ≥ v1.17 (older mage breaks on the backquotes in
the magefiles' doc comments); and non-interactive `sudo` for the two root reads
(`/sys/kernel/tracing` formats, `/sys/kernel/btf/vmlinux`).

`../libbpfgo` is relative to the main checkout, not to the
working tree: in a linked worktree (`git worktree add`, e.g.
`.claude/worktrees/agent-*`) Mage takes the main checkout from
`git rev-parse --git-common-dir` and uses its sibling libbpfgo,
so worktrees need no `LIBBPFGO`. `$LIBBPFGO` still wins when
set; without git (a source tarball) the path is relative to the
working tree. `internal/buildenv` holds the resolution and its
tests.

```bash
mage generate      # tracepoint formats -> C handlers, Go tables, Go types,
                   # per-arch syscall numbers (from golang.org/x/sys)
mage bpfBuild      # vmlinux.h (first run) + clang CO-RE -> iormesh.bpf.o
mage build         # bpfBuild + go build ./... + bin/iormesh, bin/iormeshctl
mage test          # go test ./... (the generator's C harness tests too);
                   # builds what it reads first: bpfBuild, agentBuild
mage testRace      # go test -race, all but generate and ctl, same deps
                   # (docs/testing.md, "Race detector gate")
mage vet           # go vet ./... with the cgo environment
mage lint          # golangci-lint: errcheck + staticcheck SA + unused
mage fmt / fmtCheck
mage clean         # removes bin/, the build outputs (decision #30);
                   # keeps vmlinux.h (it needs root to recreate) and
                   # dist/ (the release builds, signed after mage sign)
mage workloadTest  # the Java workload, standalone (M0)
mage bpfRootTest   # BPF root tests: loader, TCP, fork, exec watch (sudo -n)
mage integrationTest        # tracer + workload end to end, as root
mage integrationTestSerial  # the same, one test at a time
mage agentBuild    # the Java agent -> bin/iormesh-agent.jar (M3),
                   # copied to dist/ for mage sign and deploy;
                   # kept when newer than its sources
mage agentTest     # its unit tests, -javaagent smoke runs and
                   # the ring's Java-driven Go tests
```

Every build output lands in `bin/` (gitignored; decisions.md
#30), none at the repository root: `iormesh`, `iormeshctl`, the
Go peers `echopeer` and `edgepeer`, the four jars
(`workload.jar`, `workload-otel.jar`, `workload-libs.jar`,
`iormesh-agent.jar`) and the gates' test binaries
(`integrationtests.test`, `iormesh-internal.test`). The jars and
`echopeer` share the directory on purpose: the non-java-peer
scenario finds `echopeer` beside `workload.jar`, and the module
jars' manifest `Class-Path` names `workload.jar` beside them and
the vendored jars as `../workload/…`. `dist/` stays the release
output.

The Java agent (`agent/`) is built with the JDK alone, like the
workload, compiled for 17 (decision #29). Its one dependency, ASM,
is vendored as the released jar in `agent/third_party/asm` with
its license and provenance; `mage agentBuild` checks the jar's
pinned SHA-256 and relocates its classes to
`iormesh.agent.shaded.asm` (`agent/tools/Relocate.java`), so
neither build nor tests touch the network. `mage agentTest` starts
with `mage agentReproTest` (two clean builds, same bytes;
"Releases" below), then runs with the `java` on `PATH` and
again with each JDK listed in `IORMESH_AGENT_JDKS` (colon-separated `JAVA_HOME`s), which is how
each hook-table variant gets checked against its JDK: list a 17
and a 21 (`jdk17`, `jdk21`) when `java` on `PATH` is 25 or later
(`jdk25`). It ends
with `internal/agentspool`'s `TestJava*` tests (cgo-free, run with
the `java` on `PATH`), which skip under a plain `go test` because
only Mage sets the agent and workload paths they need.

The controller's terminal UI (`iormeshctl tui`, task ob4) adds
the module's one interactive-UI dependency set, pinned in
go.mod/go.sum at the versions ior uses: `charm.land/bubbletea/v2`
v2.0.1, `charm.land/lipgloss/v2` v2.0.0 and
`github.com/charmbracelet/x/ansi` v0.11.7, with
`github.com/charmbracelet/x/exp/teatest/v2` for the tests only;
their indirect charmbracelet modules (`ultraviolet`,
`colorprofile`, `x/term`, …) sit at ior's versions too. All are
MIT-licensed and pure Go, so `iormeshctl` stays cgo-free —
`internal/buildgate`'s `TestControllerIsCgoFree` and
`TestControllerCrossCompiles` (darwin/arm64, `CGO_ENABLED=0`)
check every package it links. A dependency bump is a deliberate
change: move it with ior's, rerun those two tests.

Run Go commands through Mage: bare `go test ./...` or
`go vet ./...` cannot build `internal`, which imports libbpfgo,
because only Mage sets `CGO_CFLAGS`/`CGO_LDFLAGS`.

- **Generation host**: the committed artifacts were generated on
  Linux 7.2 and match ior's byte for byte but for iormesh's
  additions (the socket hooks of connect/accept, step 4's
  `tcp_state_event`; `generated_tracepoints.go` holds only
  `List`, without ior's selector maps). On a kernel with
  another tracepoint set `mage generate` stops at the diff of
  `internal/c/generated_tracepoints_result.txt` without touching
  the tree; `IORMESH_FORCE_GENERATE=1` adopts the new set (for
  throwaway container builds — do not commit an older kernel's
  deletions).
- **vmlinux.h** is dumped from the build host's BTF and
  gitignored, as in ior; CO-RE relocations make the object
  portable anyway.
- **Attach set**: handlers are generated for every syscall, but
  only the selected event families attach: the default `file`
  and `socket` (`tracepoints.DefaultSyscalls`), plus what
  `iormesh run -trace-families` adds (`ipc`, `signal`, `poll`,
  `zerocopy`, decision #7). A new family or member is a code
  change in `internal/tracepoints/trace_family.go`, not a
  regeneration.
- **External object**: `IORMESH_BPF_OBJECT=path.bpf.o` loads that
  object instead of the embedded one.
- **No load fallback for the typed walk**: `tcpseq.c` walks a
  descriptor to its socket with `fileident.c`'s typed walk
  (`bpf_get_current_task_btf`, `bpf_rdonly_cast`) on every kernel
  that has the kfunc; on one without it the walk is pruned at load
  and captures nothing. The tracer loads the object once
  (`internal/bpf/loader.go`) and has no switch that turns the walk
  off, so a kernel with the kfunc whose verifier rejects the walk
  fails the whole load. ior's second load without its identity
  capture (`loadWithIdentFallback`) was not lifted, and iormesh
  leaves `IOR_FILE_IDENT` at 0, so that capture is off anyway. The
  walk is verified to load on Linux 7.2 and on Rocky 9.9's el9
  5.14 kernel (task nf4); a rejection elsewhere is a defect to fix
  in the walk, as milestones.md M4 says of el9 load failures.
- **`//nolint` is banned** (`internal/buildgate`): a deliberate
  discard is an explicit `_ =`; an exemption goes into
  `.golangci.yml` with its reasoning.

## CI

GitHub Actions (`.github/workflows/ci.yml`, task ck4) runs the
unprivileged gates on every push and on pull requests to main,
on `ubuntu-latest`: it builds the pinned libbpfgo (cached), then
runs `mage fmtCheck`; the kernel-independent generate steps
(`generateTracepointsGo`, `generateTypesGo`,
`generateSyscallNumbersGo`), failing on any `git diff`; then
`mage bpfBuild`, `mage vet`, `mage lint`, `mage agentBuild
workloadBuild`, `mage agentReproTest` ("Releases", below),
`mage test`, `mage testRace` and `mage workloadTest`. It uses
no secrets, a read-only token and actions pinned by commit SHA.
The runner is not the main target: the root gates (`mage
bpfRootTest`,
`mage integrationTest`), the el9 runs on Rocky 9.9 and the kind
acceptance stay manual ([testing.md](../testing.md), "CI"). So
does the C step of `mage generate`: it numbers the handlers by
the build kernel's tracepoint IDs, so only the generation host
regenerates without a diff.

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
bpftool; mage ≥ v1.17 (older mage breaks on the backquotes in
the magefiles' doc comments); and non-interactive `sudo` for the two root reads
(`/sys/kernel/tracing` formats, `/sys/kernel/btf/vmlinux`).

```bash
mage generate      # tracepoint formats -> C handlers, Go tables, Go types,
                   # per-arch syscall numbers (from golang.org/x/sys)
mage bpfBuild      # vmlinux.h (first run) + clang CO-RE -> iormesh.bpf.o
mage build         # bpfBuild + go build ./... + ./iormesh, ./iormeshctl
mage test          # go test ./... (the generator's C harness tests too)
mage testRace      # go test -race, all but generate and ctl
                   # (docs/testing.md, "Race detector gate")
mage vet           # go vet ./... with the cgo environment
mage lint          # golangci-lint: errcheck + staticcheck SA + unused
mage fmt / fmtCheck
mage clean         # keeps vmlinux.h (it needs root to recreate)
mage workloadTest  # the Java workload, standalone (M0)
mage bpfRootTest   # BPF root tests: loader, TCP, fork, exec watch (sudo -n)
mage integrationTest        # tracer + workload end to end, as root
mage integrationTestSerial  # the same, one test at a time
mage agentBuild    # the Java agent -> ./iormesh-agent.jar (M3),
                   # copied to dist/ for mage sign and deploy
mage agentTest     # its unit tests, -javaagent smoke runs and
                   # the ring's Java-driven Go tests
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
  5.14 kernel (PLAN.md, task nf4); a rejection elsewhere is a
  defect to fix in the walk, as PLAN.md says of el9 load failures.
- **`//nolint` is banned** (`internal/buildgate`): a deliberate
  discard is an explicit `_ =`; an exemption goes into
  `.golangci.yml` with its reasoning.

## Container builds

The release binaries come out of containers, with the same
targets for Docker and Podman (rootless Podman works):

```bash
mage buildPodman   # every release binary into dist/, with Podman
mage buildDocker   # the same with Docker
mage containerBuild podman tracer-amd64,ctl  # a subset (ctl,
                   # tracer-amd64, tracer-arm64; all by default)
mage containerClean podman  # drop builder images and cache volumes
mage sign          # sign dist/ with the release key (see "Signing")
```

A missing engine, or Docker's CLI without a running daemon,
fails up front with the fix named.

| Target         | Builder                                   | Output in `dist/` |
|----------------|-------------------------------------------|-------------------|
| `ctl`          | `golang:1.26.10-trixie`, as is            | `iormeshctl-{darwin,linux}-{amd64,arm64}` |
| `tracer-amd64` | Rocky Linux 9 (ior's builder)             | `iormesh-linux-amd64`, static |
| `tracer-arm64` | Debian trixie, aarch64 cross toolchain    | `iormesh-linux-arm64`, static |

- **Builders** (`scripts/container/*.Containerfile`) hold only
  the toolchain; the tree is mounted read-only and copied, so a
  build never writes into it. Inside, `go run
  github.com/magefile/mage releaseTracer` (or `releaseCtl`) runs
  on the copy — mage at go.mod's version, checked by go.sum —
  and the binaries are copied to `dist/`. The two Mage targets
  also run on a host that has the static archives (and, for
  arm64, the cross compiler).
- **Pins**: every base image by index digest (the constants in
  `internal/containerbuild`, which a test keeps in step with the
  Containerfiles); Go from the golang image, also in the Rocky
  builder; libbpfgo by tag and commit; the zstd and elfutils
  sources by SHA-256; mage by go.mod.
  Distro packages (clang, gcc, bpftool, the static libs) follow
  their repos and are not pinned.
- **libbpf**: the amd64 builder runs `make libbpfgo-static` like
  ior's; the arm64 builder builds only `libbpf.a` and its headers,
  with `aarch64-linux-gnu-gcc`, and cgo cross-compiles libbpfgo
  with the tracer (`IORMESH_TARGET_ARCH=arm64` sets `GOARCH`, `CC`,
  `CGO_ENABLED=1` and the BPF `-D__TARGET_ARCH_arm64`). Debian's
  static `libelf.a` (elfutils ≥ 0.191) lacks `eu-search.o` from
  elfutils' internal `libeu.a`; the arm64 builder compiles it from
  the matching, checksummed elfutils release and adds it.
- **Why two distros**: Rocky 9 has no aarch64 userspace cross
  compiler and no static libelf; Debian's multiarch has both as
  packages. Both tracers link fully static (`netgo,osusergo`,
  `-extldflags -static`), so the builder's glibc never reaches a
  server.
- **No generation in the container**: the generated handlers and
  tables are committed and pinned by the diff gate on the
  generation host; `mage generate` needs that host's tracefs as
  root, and a cross build could not read its target's tracepoints
  anyway. The builds therefore need no privileges — only the
  host's world-readable BTF, mounted read-only, for `vmlinux.h`
  when the tree has none. SELinux labeling is switched off for
  the run instead of relabeling the tree.
- **arm64 kernel types**: without more input the arm64 object is
  compiled against the build host's (amd64) `vmlinux.h`. CO-RE
  relocates every field by name against the running kernel's
  BTF, but the risk runs the other way round from a compile
  error: an x86-only field compiles fine and fails only when
  the object *loads* on arm64, unless the code guards it with
  `bpf_core_field_exists` (as dispatch.c does for
  `thread_info.status`). `javamark.c`'s `ctx->sp` relies on
  libbpf matching `sp` inside arm64 `pt_regs`' anonymous struct
  (it should relocate; unverified), and its exit-probe comment
  about the popped return address is x86-only reasoning. **The
  arm64 object has never been loaded on a real arm64 kernel.**
  To compile against arm64 types, dump an arm64 host's BTF
  (`bpftool btf dump file /sys/kernel/btf/vmlinux format c`)
  and point `IORMESH_VMLINUX_ARM64` at the file.
- **Checks**: each binary's header is verified (ELF machine or
  Mach-O CPU; the tracers must have no ELF interpreter), then
  every Linux binary runs `--help` on Rocky 9 of its own arch.
  arm64 runs emulated, so it needs `qemu-user-static`
  (binfmt_misc `qemu-aarch64`); without it the run is skipped
  with a notice. Module downloads and the Go build cache live in
  the `iormesh-gomod` and `iormesh-gocache` volumes.
- **The tracer image** (M7, task vb4; built and run on kind by
  task yb4): `scripts/container/tracer-image.Containerfile`
  packs a release tracer from `dist/` and `iormesh-agent.jar`
  into `FROM scratch` — no shell, no libc — with the binary at
  `/opt/iormesh/bin/iormesh` (on `PATH`), run as uid 0; traces go
  to the pod's emptyDir, one level inside it (`k8s.TraceRoot`).
  Build it from the repository root after `mage buildPodman` and
  `mage agentBuild`, one image per arch (`podman build --platform
  linux/amd64 -f scripts/container/tracer-image.Containerfile
  .`), with BuildKit or Buildah (`COPY --chmod`); the root's
  `.containerignore` (and `.dockerignore`, a symlink to it)
  sends only `dist/`'s tracers and the jar. Push it and put the
  digest in `k8s.DefaultImage` (internal/k8s) before `mage
  k8sManifests`; until then the manifests name an unpullable
  `registry.invalid` placeholder. The image has no
  `/etc/machine-id` and needs none: in a pod `iormesh run` takes
  its host id and FQDN from the downward-API env both container
  specs set (task 7f4, docs/implementation.md M7 step 6); a run
  without that env in this image fails. On kind (task yb4) it
  ran as debug container and sidecar with #13's set and
  `RuntimeDefault` seccomp (containerd 2.2.0); the static
  tracer for it can also come from `mage releaseTracer` on a
  host with the static archives (Fedora 44 had them). No image
  is published yet, so `k8s.DefaultImage` stays empty and
  `deploy/` keeps the placeholder: a deployment passes its
  pushed digest with `-image` or the registry's `k8s.image`, as
  `mage k8sAcceptance` does for its local registry
  ([testing.md](testing.md#m7-acceptance-on-kind-task-yb4)). The
  capability set it runs with is provisional (PLAN.md decision
  #13, [security.md](security.md) "Kubernetes").
- **kubectl on the controller** (k8s mode only, task wb4): 1.30
  or later. The debug container's security context and volume
  mounts go in through `kubectl debug --custom`. Checked on kind
  (task yb4): 1.31.12 and 1.35.9 take it by default; 1.30.14
  only with `KUBECTL_DEBUG_CUSTOM_PROFILE=true`, which the
  controller sets on every kubectl it runs; 1.29.15 has no
  `--custom` at all. With `--profile=general` the stored
  container has exactly the `--custom` file's capabilities on
  every version that takes the flag.
- **The `iormesh-selinux` RPM** (opt-in, task 7h4): `mage
  selinuxRPM podman` (or `docker`) builds the
  `iormesh_container_t` policy module and its RPM from
  `packaging/selinux/` in a throwaway Rocky 9 container
  (`build.sh` installs selinux-policy-devel, container-selinux
  and rpm-build there) and writes the binary and source RPMs to
  `dist/selinux/`. The build host needs no SELinux tools, and
  neither does `mage test`: `packaging/selinux/policy_test.go`
  reads the `.te` and the spec as text. Install the RPM on an
  SELinux-enforcing el9 node that has container-selinux;
  PLAN.md #13 and [security.md](security.md) say when to.

## Signing

Release binaries are signed with minisign (PLAN.md decision
#28, built with its default by task bb4) after the container
builds:

```bash
export IORMESH_MINISIGN_KEY=~/.minisign/iormesh.key  # outside the tree
mage agentBuild    # also dist/iormesh-agent.jar (task kf4)
mage sign          # dist/NAME -> dist/NAME.minisig, then verified
iormeshctl verify dist/iormesh-linux-amd64   # the controller's check
```

- **Key**: a minisign key pair made once, e.g. `minisign -G -p
  iormesh.pub -s iormesh.key`, with a password (the Go library
  reads only scrypt-encrypted secret keys). The secret key
  belongs to the release pipeline and never enters the
  repository: `mage sign` refuses a key path inside the tree.
  The password comes from the terminal prompt, or from
  `$IORMESH_MINISIGN_PASSWORD` for unattended runs. Prefer the
  prompt or a CI secret store that masks it: an environment
  variable is readable by every process of the same uid
  (`/proc/<pid>/environ`) and leaks into logs that dump the
  environment.
- **What is signed**: each file in `dist/` (not hidden, not a
  `.minisig`) — the tracers, the controllers and, after `mage
  agentBuild`, the agent jar `iormesh-agent.jar`, which
  `iormeshctl deploy` ships to hosts whose projects use the
  agent and `iormesh upgrade` installs only at the tracer's
  version (task kf4; [security.md](security.md)) — prehashed
  (BLAKE2b-512, minisign's default),
  with the trusted comment
  `timestamp:<unix> file:<name> version:<x.y.z>`; the version
  is `internal/version.Version`, bumped per release. The tracer
  and the controller both check the signed `file:` and
  `version:`, so the comment is part of what is verified.
- **Stale builds are refused** (task uf4): every artifact
  carries its own version, and `mage sign` checks all of them
  against `internal/version.Version` before it signs any. The
  jar's manifest has `Implementation-Version` (written by `mage
  agentBuild`); the tracers and controllers carry
  `version.Stamp`, `"\0iormesh-version:<x.y.z>\0"`, in their
  rodata (kept linked by the `version` subcommands), read
  statically, since the signing host cannot run an arm64 or
  darwin build to ask its `--version`. A missing or different
  stamp fails the run with `rebuild it` and leaves `dist/`
  unsigned, so a jar or tracer left from an older build is never
  signed as the new release. `TestControllerCrossCompiles` and
  `TestTracerCarriesVersionStamp` (`internal/buildgate`) keep
  the stamp in real builds. The stamp proves the version only,
  not the commit (task 8g4): a build of another commit at the
  same version would pass. No vcs revision is stamped — the
  container builds get no `.git` (`.containerignore`), so Go's
  `-buildvcs` has nothing to embed, and the jar would need its
  own — so the release rule is to bump the version, commit, and
  rebuild `dist/` from that commit right before `mage sign`.
- **Rebuilding without a version bump**: through the sudoers
  rule `upgrade` refuses a jar (as a tracer) with other bytes
  under an installed version, so a rebuilt and re-signed jar at
  the same version makes `iormeshctl deploy` fail on every host
  that already has the jar — closed, nothing is started there.
  Bump `internal/version.Version` for a release, or reinstall
  as root for a test host (docs/testing.md, the M4 runbook).
- **Library**: `aead.dev/minisign`, pinned by go.mod/go.sum
  (and `golang.org/x/crypto` for BLAKE2b and scrypt). The files
  are ordinary minisign: `minisign -Vm FILE -p iormesh.pub`
  checks them, and the `minisign` tool can sign instead of
  `mage sign` with `-t "file:NAME version:X.Y.Z"`.
- **Pinned public key**: `~/.config/iormesh/minisign.pub` on the
  controller (`iormeshctl verify`, `-key` overrides; `iormeshctl
  deploy` checks every build with it before pushing),
  `/opt/iormesh/etc/minisign.pub` (root-owned) on each server,
  where `iormesh upgrade FILE` verifies `FILE.minisig` before it
  replaces `/opt/iormesh/bin/iormesh` ([security.md](security.md)
  has the checks).

## Releases

`mage release` turns a tagged commit into downloadable archives
(PLAN.md "Release archives", task rh4). It publishes nothing:
uploading the files is a human step.

```bash
# bump internal/version.Version, commit, then:
git tag v0.1.0                # exactly v<internal/version.Version>
export IORMESH_MINISIGN_KEY=~/.minisign/iormesh.key
mage release podman           # or docker
ls dist/release/0.1.0/
# iormesh-0.1.0-amd64.tar.gz  iormesh-0.1.0-arm64.tar.gz
# SHA256SUMS  SHA256SUMS.minisig
```

- **Refused up front**: a dirty tree, a HEAD past the tag, or a
  tag other than `v<internal/version.Version>` (`git describe
  --tags --dirty --match 'v[0-9]*'` must print exactly the tag).
  The key's password is asked next, before the long builds.
- **Steps**: every container build (`containerBuild ENGINE
  all`), `agentBuild`, the signing `mage sign` does (stale builds
  refused), then `internal/dist` packs a fresh
  `dist/release/<version>/` and signs its `SHA256SUMS`.
- **Archive contents**, under `iormesh-<version>-<arch>/`:
  `iormesh-linux-<arch>`, `iormesh-agent.jar`,
  `iormeshctl-linux-<arch>`, `iormeshctl-darwin-<arch>`, each
  with its `.minisig`; `minisign.pub`; `CHANGELOG.md`; `LICENSE`
  once the repository has one (the licence is the owner's choice,
  still open). Each of the last two is packed when present, and
  a missing one is noted. Each artifact is verified against its signature at the
  release version before it is packed, and the verified bytes
  are the packed ones.
- **Reproducible**: entries sorted, owner root 0/0, modes
  0755 (binaries, the top directory) or 0644, every mtime
  `$SOURCE_DATE_EPOCH` or else the commit time, gzip header
  without name or time. The same signed `dist/` packs to the same
  bytes with the same Go toolchain (its gzip encoder could change
  between releases; `internal/dist`'s `TestPackDeterministic`);
  signing again changes the signatures' timestamps and so the
  archives.

An operator checks a download with the release key obtained
apart from the archives, here saved as `~/iormesh-release.pub`.
The `minisign.pub` inside an archive is a convenience, not a
trust anchor: use it only after comparing it with that key.

```bash
KEY=~/iormesh-release.pub                 # the published key
minisign -Vm SHA256SUMS -p "$KEY"
sha256sum -c SHA256SUMS
tar -xzf iormesh-0.1.0-amd64.tar.gz && cd iormesh-0.1.0-amd64
./iormeshctl-linux-amd64 verify -key "$KEY" iormesh-linux-amd64 \
  iormesh-agent.jar iormeshctl-linux-amd64  # every build: OK
install -D -m 0644 "$KEY" ~/.config/iormesh/minisign.pub
```

The controller checking itself proves nothing on its own; the
`minisign -Vm SHA256SUMS` step, done with a trusted `minisign`,
is what vouches for the archive it came in.

With the key pinned there, `iormeshctl deploy -dist DIR` checks
every build again before it pushes.

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

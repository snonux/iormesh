# Container builds

Part of [Build & code generation](../build.md).

The release binaries come out of containers, with the same
targets for Docker and Podman (rootless Podman works):

```bash
mage buildPodman   # every release binary into dist/, with Podman
mage buildDocker   # the same with Docker
mage containerBuild podman tracer-amd64,ctl  # a subset (ctl,
                   # tracer-amd64, tracer-arm64; all by default)
mage containerClean podman  # drop builder images and cache volumes
mage sign          # sign dist/ with your own key (see "Signing")
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
  Distro packages (gcc, bpftool, the static libs) follow their
  repos and are not pinned. clang is checked, not pinned: each
  tracer Containerfile names the clang it expects in
  `ARG CLANG_VERSION` (amd64 22.1.8 from Rocky 9.9, arm64
  19.1.7 from Debian trixie) and fails the image build when
  `clang --version` says otherwise, and `TestBuilderClangPinned`
  (internal/buildgate) holds that value to a recorded one, so
  a new clang needs the el9 verifier check first
  ([testing.md](../testing.md), "Builder clang pin", task ll4).
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
  (it does: see "arm64 status" below), and its exit-probe comment
  about the popped return address is x86-only reasoning.
  To compile against arm64 types, dump an arm64 host's BTF
  (`bpftool btf dump file /sys/kernel/btf/vmlinux format c`)
  and point `IORMESH_VMLINUX_ARM64` at the file.
- **arm64 status** (task 8k4, 2026-11-24): loaded on Rocky 9.9
  aarch64 (`5.14.0-741.22.1.el9_9.aarch64`, BTF) in QEMU on the
  x86 laptop — an **emulated CPU** (TCG, `-cpu max`), so it
  proves load, verifier and JIT acceptance and event flow on an
  arm64 el9 kernel, and nothing about timing or overhead. The
  release `iormesh-linux-arm64` from `tracer-arm64` is
  **refused by the verifier** (`handle_sys_exit_read`, "R8
  unbounded memory access" in `rwagg.c`'s exit path): the
  builder's Debian clang 19 bound-checks `idx` on a copy and
  indexes with the unchecked register. The fault is the
  compiler's, not arm64's — clang 19's bytecode is identical
  with `-D__TARGET_ARCH_x86`, and that object is refused the
  same way — so the amd64 tracer, built by the Rocky builder's
  own clang, is not affected. The same source built with clang
  22 loads with no other change: the x86 `vmlinux.h` relocated
  fine, `iormesh run` traced file and TCP I/O, and 40 of the
  root BPF gate's 42 tests passed, the Java marks' uprobes
  (`ctx->sp`) included; the other two assume x86 (task jl4;
  docs/testing.md, "arm64 on an emulated el9 kernel").
  **Fixed in the source** (task il4, decision #31): every
  variable slot or frame index goes through
  `iormesh_index_mask` (`internal/c/maps.h`), so the bound sits
  on the indexing register whatever clang makes of the check.
  The builder's clang 19 objects, `-D__TARGET_ARCH_arm64` and
  `_x86`, then loaded, attached and traced `dd`'s reads on the
  same aarch64 el9 guest, and the x86 one passed
  `TestLoaderAttachesEveryFamily` and `TestEl9RecordLayout` on
  the 7.2 laptop, whose verifier had refused it the same way.
  Task ll4 applied the same to the last two such sites
  (`filter.c`'s histogram bucket, `fdname.c`'s name length,
  the latter with `iormesh_index_clamp`), rebuilt the release
  with `mage containerBuild podman tracer-arm64` and ran the
  root BPF gate on the guest with the builder's static arm64
  test binary: the release loads and traces, and all 43 root
  tests pass (testing.md, "Builder clang pin"). arm64 has no
  open verifier issue on el9.
- **Checks**: each binary's header is verified (ELF machine or
  Mach-O CPU; the tracers must have no ELF interpreter), then
  every Linux binary runs `--help` on Rocky 9 of its own arch.
  arm64 runs emulated, so it needs `qemu-user-static`
  (binfmt_misc `qemu-aarch64`); without it the run is skipped
  with a notice. Module downloads and the Go build cache live in
  the `iormesh-gomod` and `iormesh-gocache` volumes.
- **The tracer image** (M7, task vb4; built and run on kind by
  task yb4): `scripts/container/tracer-image.Containerfile`
  packs a release tracer from `dist/` and `bin/iormesh-agent.jar`
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
  ([testing.md](../testing/m7-acceptance.md#m7-acceptance-on-kind-task-yb4)). The
  capability set it runs with is provisional (decisions.md
  #13, [security.md](../security.md) "Kubernetes").
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
  decisions.md #13 and [security.md](../security.md) say when to.

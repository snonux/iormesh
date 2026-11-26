# arm64, the clang pin and releases

Part of [Testing strategy](../testing.md).

## arm64 on an emulated el9 kernel (task 8k4, 2026-11-24)

**Emulated CPU.** The guest ran in QEMU 10.2.2 with TCG on the
x86 laptop (`-machine virt -cpu max`, 4 vCPUs, 4 GiB, UEFI from
edk2's aarch64 pflash, user-mode networking with SSH forwarded
on 127.0.0.1 only). The kernel is a real el9 aarch64 one —
Rocky 9.9 GenericCloud aarch64, image
`Rocky-9-GenericCloud-Base-9.9-20261112.0` checked against its
CHECKSUM, kernel `5.14.0-741.22.1.el9_9.aarch64` with BTF,
SELinux enforcing, cloud-init seeding a throwaway key. So this
proves load, verifier and JIT acceptance and event flow on an
arm64 el9 kernel; it says nothing about timing or overhead
(a passing root test took 4–106 s there).
The VM booted to SSH in 7.5 min and ran for 55 min in all
(09:12–10:08 UTC), then was powered off; image, overlay and keys stayed in the
session's scratch directory.

What ran:

- `dist/iormesh-linux-arm64` from `mage containerBuild podman
  tracer-arm64` (the release build, clang 19.1.7 from Debian
  trixie, compiled against the laptop's x86 `vmlinux.h`).
- `./internal`'s test binary, cross-built static in the same
  builder image (`go test -c -tags netgo,osusergo -ldflags
  '-extldflags -static'` with `IORMESH_TARGET_ARCH=arm64`'s
  environment), run as root from a copy of `internal/` with
  `testdata/btf` beside it, with `gatecmd.RootBPFTestArgs`.
- `iormesh run -project arm64 -pid <shell>` on a shell that
  runs two `dd`s (4 MiB to a file, then back) and a Python
  loopback TCP exchange (4096 bytes one way, 2 back).

Results:

- **The release object does not load.** The verifier refuses
  `handle_sys_exit_read` ("R8 unbounded memory access",
  `rwagg.c` `iormesh_rw_on_exit`): clang 19 checks
  `idx >= IORMESH_RW_SLOTS` on a zero-extended copy and indexes
  `st->slot[idx]` with the unchecked register, which the 5.14
  verifier cannot follow. It is not arm64's doing: clang 19's
  bytecode for that program is identical with
  `-D__TARGET_ARCH_x86`, and that x86 object is refused on the
  same kernel the same way. The amd64 release, built by the
  Rocky builder's clang, is unaffected. Every root test that
  loads the object fails the same way with the embedded one.
  Filed as task il4, fixed in the source (decision #31): the
  builder's clang 19 objects, arm64 and x86, then loaded and
  traced on the same guest ([build](../build.md), "arm64
  status").
- **With the same source built by clang 22.1.1**
  (`-D__TARGET_ARCH_arm64`, laptop `vmlinux.h`) and loaded
  through `IORMESH_BPF_OBJECT`, the tracer attaches ("38
  tracepoints missing on this kernel" — the legacy syscalls
  arm64 never had), and the trace holds 861 events: `dd`'s
  writes summing to 8 MiB (8388608 bytes) and its reads to
  8 MiB plus its library loads, `connect` and
  `accept4` with the loopback 4-tuple and one `conn_id`,
  `sendto`/`recvfrom` with 4098 bytes each way, `tcp_state`
  transitions, exec/fork of the children; the session record
  names `arch: aarch64`. No lost records, no unpaired exits.
- **Root BPF gate with that object**: 38 of 42 pass. Two fail
  on x86 assumptions in the tests, filed as task jl4:
  `TestLoaderAttachesEveryFamily` wants no tracepoint skipped,
  but arm64 has no `open`, `mkdir`, `rename`, `dup2`, `pipe`,
  `poll`, `epoll_wait` and the like (46 tracepoints);
  `TestEl9RecordLayout` loads the arm64 object against the x86
  el9 BTF fixture, where `handle_raw_sys_enter`'s `pt_regs`
  relocation finds nothing. `TestJavaMarkNamesTheNative`
  and `TestJavaMarkOnHeldRead` first failed only for want of
  `java`; with the guest's `java-21-openjdk-headless` both
  pass (160 s and 111 s), so `javamark.c`'s uprobes on
  libjava's natives, `ctx->sp` included, work on arm64 — 40 of
  42 in all. Task jl4 (next section) made the two arm64-aware.

The x86 `vmlinux.h` was enough for the clang-22 object:
CO-RE relocated every field the attached programs use on the
arm64 kernel, `dispatch.c`'s guarded `thread_info.status`
included.


## The root BPF gate on arm64 (task jl4, 2026-11-24)

The two tests 8k4 found x86-bound now know the arch, and stay
exactly as strict on amd64:

- **`TestLoaderAttachesEveryFamily`** lets the loader skip
  only the probes of `tracepoints.ArchAbsentSyscalls(GOARCH)`
  and fails on any other skip. The list is empty for amd64.
  For arm64 it is the 23 family syscalls the kernel's generic
  syscall table (`include/uapi/asm-generic/unistd.h`) leaves
  out: `open`, `creat`, `mkdir`, `mknod`, `rmdir`, `unlink`,
  `rename`, `link`, `symlink`, `readlink`, `newstat`,
  `newlstat`, `utime`, `utimes`, `futimesat`, `chmod`,
  `chown`, `lchown`, `dup2`, `pipe`, `epoll_create`,
  `epoll_wait` and `poll` (46 tracepoints, 8k4's skip list).
  `TestArchAbsentSyscallsMatchTable` (internal/tracepoints,
  no root) pins the list to the family syscalls without an
  entry in `internal/bpf/sysnum_<arch>.go`, generated from
  x/sys's `zsysnum_linux_arm64.go`, which is that generic
  table: no name more, none less, and none for amd64.
  `TestArchAbsentSyscallsReplacements` checks that each has
  its replacement in the families (`openat`, `mkdirat`,
  `unlinkat`, `renameat2`, `newfstatat`, `utimensat`,
  `fchmodat`, `fchownat`, `dup3`, `pipe2`, `epoll_create1`,
  `epoll_pwait`, `ppoll`, ...), and since any other skip
  fails the root test, those replacements must attach on
  arm64. `TestUnexpectedSkips` (internal/archskips_test.go,
  no root) pins the filter. Red first: the list was empty, and
  `TestArchAbsentSyscallsMatchTable` failed naming the 23.
- **`TestEl9RecordLayout`** relocates the object against the
  trimmed el9 BTF of its own arch. The new
  `internal/testdata/btf/rocky9.9-5.14.0-741.22.1.el9_9.aarch64.min.btf`
  (10,747 bytes) is `bpftool gen min_core_btf` of the 8k4
  guest's `/sys/kernel/btf/vmlinux` (sha256 `1167cf6c…`,
  kernel `5.14.0-741.22.1.el9_9.aarch64`) for the arm64 object
  (`IORMESH_TARGET_ARCH=arm64 mage bpfBuild`, clang 22.1.1);
  adding 8k4's clang-19 arm64 object to the trim gives the same
  file. el9's aarch64 `raw_syscalls` records have the same
  layout as x86's (id 16, args and ret 24, tracefs format). On
  an arch without a file the test skips naming it; a listed
  file that is missing still fails, as on amd64 before.

Runs: on the x86 7.2 laptop both root tests pass unchanged
(`sudo -n -E` on `./internal`'s test binary, `-test.run` for
the two). In the 8k4 aarch64 guest (booted once for this, 2
vCPUs under TCG, 10:27–10:39 UTC, then powered off), the
static arm64 test binary with the clang-22 object embedded
(cross-built in the `tracer-arm64` builder image) passes
both: 46 probes skipped as expected, every other one attached;
record loads at 16/24 against the trimmed aarch64 BTF and
against the guest's own BTF. Not covered: that the arm64
replacements deliver records (only that they attach), and the
gate with the clang-19 object (task il4).


## Builder clang pin (task ll4, 2026-11-24)

Decision #31 puts every variable array index's bound on the
indexing register (`iormesh_index_mask`, and
`iormesh_index_clamp` where the bound is no power of two), so
the source should load whatever clang builds it. The release
builders still check their clang: `ARG CLANG_VERSION` in
`scripts/container/tracer-amd64.Containerfile` (22.1.8, Rocky
9.9) and `tracer-arm64.Containerfile` (19.1.7, Debian trixie)
fails the image build when the distro's `clang --version`
differs, and `TestBuilderClangPinned` (internal/buildgate, no
root, CI-safe) fails when a Containerfile's value is not the
one recorded in its `builderClang`. Red first: with the arm64
entry recorded as 22.1.1 it failed with
`scripts/container/tracer-arm64.Containerfile installs clang
19.1.7, recorded "22.1.1": re-run the el9 verifier check ...`.
`TestContainerfileClangRejects` covers a missing, a doubled and
an unchecked `ARG`.

**The el9 verifier check**, to run before changing either
value (the image build has already failed, or a newer clang is
wanted):

1. Compile the object with the new clang, for the builder's
   arch — `mage containerBuild podman tracer-<arch>` with the
   new `CLANG_VERSION`, or the distro image with only clang
   installed (`clang -g -O2 -Wall -fpie -target bpf
   -D__TARGET_ARCH_<x86|arm64> -I<libbpfgo>/output -c
   internal/c/iormesh.bpf.c`).
2. Load it on el9: on an x86 el9 host, or in the emulated el9
   aarch64 guest of "arm64 on an emulated el9 kernel" above,
   run `gatecmd.RootBPFTests` as root with the release's static
   test binary, or at least `TestLoaderAttachesEveryFamily`
   and `TestEl9RecordLayout` with `IORMESH_BPF_OBJECT` set to
   the new object. `TestLoaderAttachesEveryFamily` loads every
   program, so a verifier refusal fails it.
3. Only when that passes, update `CLANG_VERSION` and
   `builderClang` together, and record the run here.

**The two remaining clamp-then-index sites** (both loaded
before, but only because of how clang 19 and 22 happened to
compile them; `llvm-objdump -d -l` of the clang-19.1.7 object
before and after):

- `filter.c` `ior_update_syscall_aggregate`: the
  `bucket_idx >= IOR_HISTOGRAM_BUCKETS` clamp emitted no
  instruction at all — the index was a constant per branch of
  `ior_histogram_bucket_index`. Now `r7 &= 0x7`
  (`iormesh_index_mask`) on the register that indexes
  `duration_histogram` in the map value and in `fresh` on the
  stack.
- `fdname.c` `ior_copy_leaf_name`: `IOR_FD_NAME_LENGTH` is 68,
  no power of two, and a mask with 127 would not keep `name[len]`
  inside the field. `iormesh_index_clamp` emits
  `if r7 <= 0x43 goto +1; r7 = 0x43` as inline asm on the
  register that is both `bpf_probe_read_kernel`'s size and the
  terminator's index.

**Runs.** `mage containerBuild podman tracer-arm64` rebuilt
the release from this source in 61 s (the new
`CLANG_VERSION` step printed "Debian clang version 19.1.7
(3+b1)"; the embedded object's DWARF names that clang), and
`./internal`'s static arm64 test binary was cross-built in
the same image as in 8k4. The 8k4 aarch64 el9 guest
(`5.14.0-741.22.1.el9_9.aarch64`, SELinux enforcing) was
booted once for both, 4 vCPUs under TCG at `nice 10`, 4 GiB,
SSH on 127.0.0.1 only: up 11:02–11:20 UTC (17 min), then
powered off, and no `qemu-system-aarch64` process remained.

- The release `iormesh-linux-arm64` traced 8k4's target (two
  `dd`s and a loopback TCP exchange): attached with the 38
  tracepoints arm64 lacks missing, 860 events, `arch:
  aarch64`, no lost records, no unpaired exits.
- **Root BPF gate with the release's clang-19 object: 43 of
  43 pass** (`gatecmd.RootBPFTests` as root, from a copy of
  `internal/testdata/btf`, 11:05–11:18 UTC; `-test.timeout=3h`
  instead of 5m for the emulated CPU).
- Rocky 9.9's clang 22.1.8 (the amd64 builder's) objects,
  `-D__TARGET_ARCH_arm64` and `_x86`, pass
  `TestLoaderAttachesEveryFamily` and `TestEl9RecordLayout`
  through `IORMESH_BPF_OBJECT` on the same guest; the x86 one
  passes both on the 7.2 laptop too. That is the el9 check
  behind the amd64 value; the amd64 release itself was not
  rebuilt or run on an x86 el9 host.
- `mage bpfRootTest` on the 7.2 laptop passes.


## Release dry run v0.1.0 (task 7k4, 2026-11-26)

The first release candidate, cut as a dry run and checked as an
operator would, following [build.md](../build/signing-releases.md#releases) and
[guide chapter 3](../guide/03-release.md) literally. Nothing was
published: the `v0.1.0` tag was local only, on `c10b0a3` (commit
time 2026-11-26 09:58:16 +0200) in a git worktree, never
pushed, and deleted again (`git tag -d v0.1.0`) after the runs;
no upload, no GitHub release. Releases ship unsigned
(decisions.md #32), so the run needed no key or password.

- **Release**: `LIBBPFGO=… nice -n 10 mage release podman`
  exited 0 in about three minutes on the Fedora laptop (builder
  images cached from earlier work). Every Linux binary passed
  its `--help` smoke run on Rocky 9 of its own arch (arm64
  emulated), none skipped. Output, `dist/release/0.1.0/`:

  | File | Bytes | SHA-256 |
  |---|---|---|
  | `iormesh-0.1.0-amd64.tar.gz` | 26626747 | `0c9ec424…71f3c` |
  | `iormesh-0.1.0-arm64.tar.gz` | 24621304 | `2d90a316…772c6` |
  | `SHA256SUMS` | 186 | `106ad907…ddbdb7` |

  Each archive holds, under `iormesh-0.1.0-<arch>/`, root 0/0,
  every mtime the commit time: `CHANGELOG.md` (5524),
  `LICENSE` (1068), `iormesh-agent.jar` (291845, 0644), and
  0755 `iormesh-linux-<arch>` (amd64 32260664, arm64 30229584),
  `iormeshctl-darwin-<arch>` (23586176, 22130898),
  `iormeshctl-linux-<arch>` (23404150, 21898733) — what the
  guide's checkpoint lists.
- **Checksums**: in a fresh scratch directory, `sha256sum -c
  --ignore-missing SHA256SUMS` and plain `sha256sum -c` printed
  `OK` for both archives.
- **Sign**: with a throwaway key pair (below),
  `./iormeshctl-linux-amd64 sign -key KEY .` in the unpacked
  amd64 archive (password from `$IORMESH_MINISIGN_PASSWORD`)
  signed the four builds at 0.1.0, `4 files signed by key
  E2864DAA8B31E755`; `CHANGELOG.md` and `LICENSE` got no
  `.minisig`. The same controller signed the unpacked arm64
  archive the same way.
- **Verify**: `verify -key PUB` printed `OK, signed <file>
  0.1.0` for all four amd64 builds and for the arm64 tracer
  copied with its `.minisig` next to them (the guide's
  both-arches step). Refusals, exit 1 each: `LICENSE` (`open
  LICENSE.minisig: no such file or directory`), the jar with
  one byte appended (`the file does not match its signature`),
  and the tracer against the laptop's real pinned key (`signed
  by key E2864DAA8B31E755, the pinned key is …`). `deploy` was
  not run: no hosts in a dry run.
- **Determinism**: a second `mage release podman` rebuilt
  every container binary and gave byte-identical archives and
  `SHA256SUMS` (`cmp`). The jar was **not** rebuilt in that
  run (`agentBuild` found it fresh); removing
  `bin/iormesh-agent.jar` and running `mage agentBuild` again
  (OpenJDK 25.0.5) gave other bytes (`fdc86361…` against
  `5779b517…`): each zip entry carries its build time and the
  nested bridge jar differs with it. Filed as task bm4, since
  fixed ("Reproducible agent jar" below).
- **Refusal**: with the tag deleted, `mage release podman`
  stopped up front: `release: no v0.1.0 tag reachable from
  HEAD`.

Where the docs and reality differed (fixed in this commit):

- The guide's key step needs the `minisign` CLI, and nothing
  said that iormeshctl cannot make a key or where minisign
  comes from; it is not installed on this laptop, so the
  throwaway key came from a ten-line Go helper outside the
  repository (`aead.dev/minisign` v0.3.0 `GenerateKey` and
  `EncryptKey`, the library iormeshctl uses), and `minisign
  -Vm` was not run. build.md "Signing", guide chapters 2 and 3
  now say so.
- Guide chapter 2 listed the repository, Go, mage and Podman as
  laptop prerequisites, though chapter 3 lets an operator
  download a release; they are marked as needed only to build
  one.
- build.md and chapter 3 offered "a rebuild of the tag compared
  byte for byte" as a trust check, which the jar defeated (task
  bm4); they said binary by binary until bm4 restored the
  byte-for-byte claim.

Everything else ran as written: the `ls` of
`dist/release/0.1.0/`, the archive contents, the `sign` output
format, `verify`'s pinned key and `-key`, and the both-arches
step.


## Reproducible agent jar (task bm4, 2026-11-26)

The dry run above found the agent jar rebuilt to other bytes.
The magefiles now pack every jar with `jar --create --date`
(`jarCreate`), dated `$SOURCE_DATE_EPOCH` or else the commit
time, the date the release archives use
([build.md](../build/signing-releases.md#releases), "Reproducible").

- **Red first**: `mage agentReproTest`, which builds the agent
  jar from scratch twice and compares SHA-256s, failed without
  `--date` (`1847d87f…` against `ebeec05e…`, OpenJDK 25.0.5);
  so did `rm -rf agent/build bin/iormesh-agent.jar; mage
  agentBuild`, twice (`9c5e564d…` against `7d276ba0…`).
- **After**: both gave `4b46f8ef…` twice, and a copy of the
  tree at another path, without `.git` and with
  `SOURCE_DATE_EPOCH` set to the commit time, the same bytes.
  `mage workloadBuild workloadOtelBuild workloadLibsBuild` run
  twice gave identical workload jars too.
- **Freshness** (task yk4): a second `mage agentBuild` kept the
  jar (its mtime unchanged); a touched input rebuilt it, and so
  did another `SOURCE_DATE_EPOCH`, whose jar's entries then
  carried that date (`internal/dist` `JarDated`).
- **Entry order**: the jar tool of JDK 17.0.9 and of 25.0.5
  both sort a directory's entries by name whatever order the
  file system lists them in. `internal/dist`'s
  `TestJarToolReproducible` packs one tree written in two orders
  with the `jar` on `PATH` (skipped without one) and wants the
  same bytes.
- **Gap**: the builder's JDK is not pinned; javac's output and
  the manifest's `Created-By` follow it, so a rebuild matches
  only with the JDK release the published jar names.

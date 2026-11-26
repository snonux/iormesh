# el9 runbook and record layout

Part of [Testing strategy](../testing.md).

## el9 runbook and runs

Rocky 9.9 is the main target, and some evidence exists only as
far as the 7.2 laptop and the trimmed el9 BTF can give it. To
run the root suites on el9, build `mage bpfRootTest`'s test
binary (and the integration binary) statically on the laptop,
stage it on `iom1` and run it serially as root, then remove it
— the procedure of "Root suites on Rocky 9.9 (task nf4)" above.
The VMs are started by the owner only.

Stage `internal/testdata/btf/` next to the binary too
(`<dir>/internal/testdata/btf/`): `TestEl9RecordLayout` reads
the trimmed el9 BTF from there and fails without it.

**Root BPF gate on el9 (task rj4, 2026-11-23): 41 of 41
pass.** On `iom1` (5.14.0-741.22.1.el9_9), the static
`./internal` test binary built on the 7.2 laptop (as in nf4,
with the BPF object embedded) ran every test of
`gatecmd.RootBPFTests` as root, staged under `/root/rj4` and
removed afterwards; iom1 was started for the run and stopped
again. It covers oh4's mmsg loop, which until then was only
relocated against the el9 BTF:

- `TestMsgCallsTCP` and `TestMsgCallsUDP` pass: sendmsg,
  recvmsg, sendmmsg and recvmmsg move the expected bytes on el9.
- `TestMmsgVerifierHeadroom` passes: el9 reports
  `verified_insns` (backported; upstream has it from 5.16), and
  its 5.14 verifier processes 1,002 instructions for each mmsg
  enter handler and 42,078 for the sendmmsg exit, against the
  1M limit — the same as 7.2.
- `TestEl9RecordLayout` passes against the host's own BTF as
  well as the trimmed one (both give the same record offsets).

Nothing is pending on el9 for the root BPF gate.

**Both root gates on el9 (task 2k4, 2026-11-26): 43 of 43 and
54 of 54 pass.** On `iom1` (5.14.0-741.22.1.el9_9.x86_64), at
6597008, with no test or subtest skipped. The first integration
run on el9 since nf4 (2026-11-21); it adds
`TestListenPortFollowsRestart`, `TestJavaLateAttach`, the Java
attach tests after 7l4/9l4, and every correlator, agent and BPF
fix since. The binaries were built on the 7.2 laptop as in nf4
(the static tracer, `internal.test` and `integrationtests.test`
with the cgo env plus `-tags netgo,osusergo -ldflags '-w
-extldflags "-static"'`; `iormeshctl`, `edgepeer`, `echopeer`
and the four jars from `mage build agentBuild workloadBuild
workloadOtelBuild workloadLibsBuild`), staged under `/opt/2k4`
with `bin/`, `workload/{scenarios,src,libs,otel}`,
`eval/questions` and `internal/testdata/btf` (iom1 has no `tar`;
`rsync -aR` copies the tree), run serially as root with
`IORMESH_IT_KEEP` set, removed afterwards; iom1 was started for
the run and stopped again.

| Gate | Result | Wall time |
|---|---|---|
| Root BPF (`gatecmd.RootBPFTests`) | 43 of 43 pass | 81 s |
| Integration (`gatecmd.IntegrationTests`, serial) | 54 of 54 pass | 218 s |

The integration binary ran with `gatecmd.IntegrationTestArgs(0)`
minus `-test.failfast`, so a failure would not have hidden the
tests after it. Newly covered on el9: `TestMsgCallsPeers` (hl4)
and `TestPeekCountedOnce` (ok4) pass. `TestMmsgVerifierHeadroom`
reports 993 and 992 instructions for the sendmmsg and recvmmsg
enter handlers and 42,659 and 42,684 for their exits (rj4:
1,002 and 42,078), still far below the 1M limit.


## el9 record layout (task of4)

lb4's dispatch bug, the raw_syscalls record read at upstream's
offsets on el9, showed only on el9. Two root tests in `mage
bpfRootTest` now hold the fix on a 7.x host:

- **`TestEl9RecordLayout`** (`internal/btf_root_test.go`)
  relocates the dispatchers and close's two handlers against a
  committed el9 BTF, set as libbpf's `btf_custom_path`. Their
  record loads must sit at id 16 and args/ret 24. Relocated
  against the host BTF, they must match the host's tracefs
  format file (8 and 16 on 7.2). The host kernel refuses the
  el9-relocated programs: their BTF-typed `task_struct` loads
  carry el9 offsets. So the test reads libbpf's relocated
  instructions (`bpf_program__insns`, `internal/corereloc`), not
  the loaded ones. With `preserve_access_index` dropped from the
  enter struct it fails: id is then read at 0, the pt_regs slot,
  and args at 8. Each program must read its field at the wanted
  offset and nothing of the record below it; a later load is no
  regression (task bg4). A relocation the trimmed BTF cannot
  satisfy makes libbpf poison the instruction (a call with imm
  `0xbad2310`); the test then fails with a hint to regenerate
  the BTF instead of reporting wrong offsets. The data calls'
  enter and exit handlers (tail-call targets the dispatchers
  do not cover, reading `tcp_sock` fields for `tcp_seq`) are
  relocated against the same BTF and checked for poison only
  (task wi4; the four msghdr calls' handlers too since task
  oh4). A `mage test`
  gate (`internal/buildgate/testonly_test.go`) keeps
  `internal/corereloc` out of every non-test file.
- **`TestDispatchSeesSeveralKinds`** traces an open/read/close
  loop and requires more than one syscall kind, and all three
  enters. The el9 bug sent nearly every call to the read
  handler.

The BTF is
`internal/testdata/btf/rocky9.9-5.14.0-741.22.1.el9_9.x86_64.min.btf`
(11 KB), trimmed to what the object's CO-RE relocations name. It
was extracted from the Rocky kernel-core RPM
(`BaseOS/x86_64/os/Packages/k/kernel-core-5.14.0-741.22.1.el9_9.x86_64.rpm`
on the Rocky 9.9 mirror, sha256
`8a339817a1d896907da0a776a00f5a022bf73aa84f1e8983f1da2c314fe81294`,
as the repository's `primary.xml` lists it); a trim of iom1's
live BTF is byte-identical. To regenerate it after `mage
bpfBuild` (needed when the four programs gain relocations
against types the file lacks):

```sh
sha256sum kernel-core-5.14.0-741.22.1.el9_9.x86_64.rpm  # 8a339817…
rpm2cpio kernel-core-5.14.0-741.22.1.el9_9.x86_64.rpm | cpio -idm
scripts/extract-vmlinux lib/modules/*/vmlinuz > vmlinux  # kernel tree
bpftool gen min_core_btf vmlinux el9.min.btf internal/c/iormesh.bpf.o
# or: ssh root@iom1 cat /sys/kernel/btf/vmlinux > el9.btf
```

The test picks the file by `GOARCH` (`el9BTFs` in
`internal/btf_root_test.go`): the object's `pt_regs` accesses
follow `__TARGET_ARCH`, so an arm64 object finds no field in
x86's BTF. The aarch64 one,
`rocky9.9-5.14.0-741.22.1.el9_9.aarch64.min.btf`, is trimmed
the same way from the emulated el9 aarch64 guest's
`/sys/kernel/btf/vmlinux` for the object `IORMESH_TARGET_ARCH=arm64
mage bpfBuild` builds (task jl4, previous sections).

The nf4 review changed `TestProcessExitGroupDead` in the same
change-set. It waits for the record it needs, then for a 200 ms
quiet period, instead of a fixed second. Where
`sched_process_exit` has `group_dead`, read from the format file,
it requires exactly one `group_dead` record; the el9 fallback
keeps "at least one". The child locks its main thread in
`init()`. `TestJavaMarkNamesTheNative` reports a missing
off-launcher-thread mark in an error of its own, which names
commit 46ba0e49.

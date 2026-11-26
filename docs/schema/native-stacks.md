# Native stacks (M2 step 9)

Part of [Trace schema (v1, frozen)](../schema.md).

`iormesh run -native-stacks` records the user stack each traced
thread had when it entered a syscall; without the flag nothing
below exists and no event has `stack_id` (decision #25: it is
not free). An event's `stack_id` names one line of
`stacks.ndjson` in the same trace directory. A worker writes
each stack as it first appears and the tracer waits for it
before it exits, so a trace that ended normally has every
stack its events name; a crash can lose the stacks still
queued:

```json
{"schema_version":1,"stack_id":3,"pid":4711,"pid_incarnation":893115330000000,
 "frames":[{"addr":"0x408a8e","module":"/usr/bin/echopeer","offset":35470,
            "build_id":"f77d64fd…","symbol":"internal/runtime/syscall/linux.Syscall6+0xe"},
           {"addr":"0x4968e6","module":"/usr/bin/echopeer","offset":616678,
            "build_id":"f77d64fd…","symbol":"syscall.Syscall+0x45"},
           {"addr":"0x7f3c2d41b2a0"}]}
```

| Key | Type | Meaning |
|-----|------|---------|
| `schema_version` | int | always `1` |
| `stack_id` | int | the id events use, from 1, unique in the trace |
| `pid`, `pid_incarnation` | int | whose address space the addresses are in |
| `frames` | array | innermost first: the frame that made the syscall, then its callers (at most 64) |
| `frames[].addr` | string | the instruction address, hex (`0x…`): JSON numbers cannot hold 64 bits |
| `frames[].module` | string, optional | the mapping's path as `/proc/<pid>/maps` names it, or a pseudo mapping (`[vdso]`); absent for anonymous memory — JIT code |
| `frames[].offset` | int, optional | the address's offset in `module`'s file; set exactly for file mappings |
| `frames[].build_id` | string, optional | the module's GNU build id, hex |
| `frames[].symbol` | string, optional | the enclosing function from the module's ELF symbol tables, `name+0xN` (raw, not demangled) |

How it is captured and what it can and cannot say:

- **Kernel**: every emitted syscall enter first sends a small
  record with `bpf_get_stackid(BPF_F_USER_STACK)`'s id into a
  stack map of 8192 buckets (`internal/c/stack.c`). User stacks
  only: at a `sys_enter` tracepoint the kernel stack is the
  syscall entry path. The map deduplicates, so each distinct
  stack is read back once. It never overwrites a bucket, so an
  id stays valid for the session. A stack the map refuses — its
  bucket holds another stack, or the map is full — is sent with
  its frames instead (`bpf_get_stack`, a 552-byte record) and
  deduplicated by them in the tracer, so it gets its `stack_id`
  all the same; collisions grow with the square of the distinct
  stacks, a few per couple of hundred. Only a walk that unwinds
  nothing leaves the event without `stack_id`. The run's
  summary line counts both: `native stacks N (inline I;
  uncaptured M: collisions C, faults F, other O)`.
- **Not every event has one**: calls folded into a `read`/`write`
  with `args.count` (M2 step 11) and a held-back call's enter the
  fold sends later carry none, nor do synthetic events (`drop`,
  `tcp_state`, `signal`, `exec`).
- **Ids are per process image**: the same kernel stack in two
  processes, or before and after an exec, gets two ids, since
  the addresses mean different code. The stack record carries
  the image's `mm->arg_start`, which every exec draws anew and
  `/proc/<pid>/stat` shows as field 48.
- **Symbolization** happens in the tracer. At an image's first
  stack it reads `/proc/<pid>/maps` and believes it only when
  `/proc/<pid>/stat` shows the record's incarnation and image
  tag before and after — so a process that exec'd, exited or
  whose pid was reused before the tracer got to the record
  keeps bare addresses, never another image's names. The maps
  are re-read when a later address lies outside every mapping
  (a `dlopen`); an address that stays uncovered is remembered,
  not re-read for. The mapped files are opened right then
  through `/proc/<pid>/map_files/<range>` — the very file the
  mapping holds, no path lookup the traced process could steer
  — with `O_NONBLOCK` (which stops a FIFO or device from
  blocking, though not a regular file on a hung FUSE or NFS
  mount: such a target can still stall the tracer while
  `-native-stacks` is on, visible as loss), and only regular
  files are kept; a worker
  goroutine parses them (`.symtab`, `.dynsym`, build id) with a
  panic guard, since the files are the traced process's. Parsed
  files are cached by device, inode, size and mtime for the
  session, so a reused inode is another file. A stripped binary
  keeps module, offset and build id, which is what an offline
  symbolizer needs (`addr2line -e FILE OFFSET` after mapping the
  offset through the file's segments). The table forgets a
  process at its exec and exit, so a long session holds only the
  live ones.
- **Walks follow frame pointers**. Go binaries and C built with
  frame pointers (Fedora's default) unwind to `main`; the JVM's
  own C++ frames (libjvm) do too, but interpreted and
  JIT-compiled Java frames lie in anonymous memory, stay raw
  addresses, and the walk through them is unreliable. Java
  attribution comes from the agent (M3, `java.stack`), never
  from here.

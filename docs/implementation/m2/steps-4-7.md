# M2 steps 4–7: files, paths, fd lifecycle, IPC

Part of [M2 — full trace surface](../m2.md), in [Implementation
guide](../../implementation.md).

4. **File surface** (done, task qa4): `tracepoints.DefaultSyscalls`
   appends the file surface to M1's set (decision #7: file on by
   default) — create/delete, rename and links, the stat family,
   truncate, timestamps, chmod/chown, xattrs, positional and
   vectored I/O, and `dup*`, which `file-attrs` declares (step 6
   adds `fcntl`). Left out: `access`/`faccessat` (fired at
   syscall frequency, decision #25), `getdents`, `lseek`,
   `fsync`, mounts, zero-copy (step 7's opt-in `zerocopy`).
   Each name is a kind in
   `internal/schema/kinds_file.go` (a unit test keeps attach list
   and kinds in step). The correlator keys the exit on the enter's
   record shape — fd, open, path or name event (`files.go`):

   ```go
   switch en.shape { // an integer compare first: read/write pay nothing
   case shapeOpen: return nil, c.openDetail(ev, p, en, ret)
   case shapePath: return nil, c.pathDetail(ev, p, en, ret)
   case shapeName: c.nameDetail(ev, p, en, ret); return nil, true
   }
   ```

   A path kind's `fd` is its dirfd (`-1` for `AT_FDCWD`); relative
   paths resolve against a dirfd the trace saw opened, an empty
   one (`AT_EMPTY_PATH`, `utimensat(fd, NULL)`) names the fd's own
   file; the cwd is not tracked. `flags` appear only where the
   syscall has them (`statx`'s `AT_SYMLINK_NOFOLLOW`). A symlink's
   text stays unresolved; opens through a symlink keep the link's
   path (resolving it needs the inode capture, not attached). The
   dup kinds create the new fd instance with the old one's path;
   step 6 completes the copy. The three goldens load and pass
   (`TestFileLifecycle`, `TestFileLinks`, `TestFileAttrs`); JDK 25
   issues `statx`, as declared. `file-attrs` now declares
   `fchmodat`'s name resolved against its directory fd, and
   `fchown`'s and `fsetxattr`'s fd paths.
   Not measured yet: S3's decision #25 budget was settled on M1's
   nine syscalls; this step adds about 60 default tracepoints, and
   `newstat`, `statx` and `newfstatat` fire often in a JVM. The
   `spikes/s3/measure.sh` run against #25 is due before M2's
   acceptance (step 11).

5. **Rename-aware paths** (done, task qa4, `paths.go`): each fd
   instance a traced open (or dup) named keeps its absolute path;
   a successful `rename*` rewrites every fd at or below the old
   name, in every traced process (a rename is filesystem-wide),
   and adds an `opened` edge to the new name. Matching is by name,
   not inode: the rename record has no inode, and a directory
   rename moves files of many inodes. What a name match could get
   wrong — a name reused by another file — is narrowed, not
   closed, by marking the fds of an unlinked name, or of a name a
   rename replaced, gone. It stays open for an unlink, recreate or
   rename by an untraced process (no record), a cwd-relative
   unlink or rename (absolute fd paths cannot match it),
   `renameat2(RENAME_EXCHANGE)` (the record's flags are always 0:
   the fds at the new name are marked gone instead of swapped),
   and processes in different mount namespaces (one name, two
   files — M7's containers). The fd metadata kinds (`ftruncate`, `fchown`, `f*xattr`,
   …) report the current path; `read`/`write` carry none
   (decision #25). Unit tests cover a directory rename, a
   cross-directory move, a prefix-sharing sibling, a failed rename
   and reuse after unlink; `file-lifecycle` declares its
   `ftruncate` after the rename on `b.dat`, so the golden proves
   it end to end (`TestGoldenFileLifecycle` offline too).

6. **fd lifecycle** (done, task ra4, `internal/correlate/fdtable.go`,
   modelled on ior's `fdTracker`): each process's fd table is the
   `procState` maps — generation, path, socket binding and
   close-on-exec state per fd.
   - `dup`, `dup2`, `dup3` and `fcntl(F_DUPFD, F_DUPFD_CLOEXEC)`
     make a new instance of the returned number (new `fd_gen`)
     naming the old fd's path *and* socket, so the copy's reads
     and writes join the same file and connection; its
     close-on-exec state is the call's (`dup3`'s `O_CLOEXEC`,
     `F_DUPFD_CLOEXEC`). `dup2` onto itself creates nothing.
     `dup3` reports `flags`, `fcntl` its command as `args.cmd`.
   - Fork (`task_newtask` with the creator's tgid): the child
     gets a copy of the parent's table — same instances and
     generations, one lineage, so the child's next instance of
     a number continues after the parent's — and the graph a
     `forked` edge parent → child. A `CLONE_FILES` sharer is
     copied too (not modelled as shared).
   - Exec (`sched_process_exec`): fds whose close-on-exec state
     is set are closed; so are unknown ones (predating the trace
     or from an untraced syscall), as in ior — keeping an fd the
     kernel closed would hand its path to the next untraced
     instance of the number — except unknown stdio 0–2. The
     state comes from the open and `accept4` flags, `dup3`,
     `F_DUPFD_CLOEXEC` and `fcntl(F_SETFD)`/`F_GETFD`; the JDK
     asks `F_GETFD` of the fds it redirects onto a child's stdio,
     so those are known; the pipes it hands its spawn helper stay
     unknown and get a new generation after the exec (an unknown
     inherited fd above 2 also loses its path and socket).
   - Exit (`sched_process_exit` with `group_dead`, task xi4,
     `internal/correlate/reap.go`): the process is marked dead at
     once, so unlinks and renames skip it and a rename after its
     death gives it no `opened` edge. Its table goes one fuse
     window later: a sibling thread's folded read/write records,
     flushed at that thread's own exit, can trail the first
     `group_dead` record (two threads may both report it on el9's
     fallback) and still need their fd instance. Its
     identity (incarnation, comm) stays while a TCP socket it
     owns is still open — the kernel reports the socket's last
     transitions after the exit, owned by the dead pid, and their
     `fd_gen` and conn id live on the socket — then for one fuse
     window, and is deleted after that. A new process under the
     pid replaces the state at its `task_newtask` or attach as
     before. Tests: `TestDeadChildrenLeaveNoState`,
     `TestLateTCPStateOfDeadProcess`, `TestPIDReusedBeforeReap`,
     `TestSiblingFlushAfterGroupDead`,
     `TestRequeuedDeadProcessWaitsLatestWindow`.
   - `close_range` is not attached: the JDK 25 child closes its
     fds one by one through `close` (strace-measured). A
     program using it keeps those fds in the table.
   `fcntl` joins the default attach set; a JVM calls it while
   setting descriptors up, not per I/O, and the #25 measurement
   due at step 11 covers it. Unit tests: every copy form, a
   copy onto an open number and of a socket, fork inheritance
   and isolation, exec per close-on-exec source
   (`fdtable_test.go`).
   Normalizer: a process without markers that a bracketed
   process forked inside its bracket — by a `forked` edge, with
   its first event inside the parent's bracket — is cut to the
   parent's window, transitively (`inheritSpans`,
   `TestBracketKeepsForkedChildren`); `prune` keeps a `forked`
   edge when both processes have bracketed events.
   Scenario: `fd-lifecycle` — fd reuse, a `dup` (the JDK's only
   in-process one: `Files.newDirectoryStream`) and stdin/stdout
   inherited by a child JVM (`ProcessBuilder` redirects, `dup2`
   onto 0 and 1 in the child, between jspawnhelper's exec and
   the child JVM's). Its golden now also declares the `forked`
   edge `$p1` → `$p2`. `TestFdLifecycle` passes it under the
   real tracer; `TestForkedChildrenKept` shows pipe-ipc's and
   signals' children kept by the cut (their goldens are step
   7's). The golden format still has no field for a copy's
   returned fd: `fd-lifecycle` declares it through its close.

7. **IPC, signals, UDP, epoll** (done, task sa4): families after
   ior's `-trace-families` concept, defaults per decision #7.
   - **Families** (`internal/tracepoints/trace_family.go`):
     `file` (M1's opens and fd I/O, steps 4–6) and `socket`
     (connect, accept*, bind, sendto, recvfrom) on; `ipc` (pipe,
     pipe2, socketpair), `signal` (kill, tgkill, tkill plus the raw
     tracepoint `signal_generate`), `poll` (epoll_create*,
     epoll_ctl, epoll_wait/pwait/pwait2, poll, ppoll) and
     `zerocopy` (sendfile64, splice, tee, copy_file_range) opt-in:
     `iormesh run -trace-families signal,ipc` (or `all`).
     `bpf.ProbesFor(families)` is the attach set; lifecycle's
     control probes always attach. The flag adds, it cannot drop
     file or socket. ior's own family names (FS, Network, …) are
     not accepted: every syscall of a family here has a schema kind
     and a correlator shape, which ior's broader groups lack.
     Not attached yet: select/pselect6, vmsplice. The default set
     grows by bind, sendto and recvfrom — and, since task oh4, by
     sendmsg, recvmsg, sendmmsg and recvmmsg, whose bytes and
     `tcp_seq` are captured (the mmsg calls' bytes summed from
     their mmsghdr arrays, `internal/c/mmsg.c`), and since task
     nk4 their datagram peer from the msghdr's `msg_name` (the
     mmsg calls' first message's; `IORMESH_ADDR_MSGHDR`).
     glibc's send(3)/recv(3) are these syscalls, so a stream
     socket's sendto/recvfrom/msghdr call sends no address
     record (`IORMESH_ADDR_NOT_STREAM`; its connection already
     has the addresses) and pays only the enter-time park of the
     argument by tid; each datagram costs a second, 232-byte
     record. Step 11's #25 measurement covers both.
   - **Socket addresses** (`internal/c/sockaddr.c`): the generated
     handlers of bind, connect, sendto and recvfrom call
     `iormesh_addr_on_enter` (parks the address argument by tid) and
     `iormesh_addr_on_exit`, which sends a `sock_addr_event` right
     before the exit record: the socket's own addresses (read from
     the `struct sock`), the argument (read by its own family's
     size, only as far as its parked length — recvfrom's
     value-result length is read back at exit, 0 means no address;
     a sun_path is cut at the length, abstract names included) and,
     for AF_UNIX, the path and `unix_sk(sk)->peer`. The accept exits report an
     accepted AF_UNIX socket the same way; TCP keeps sock.c's
     state records. The msghdr calls (task nk4) park their
     msghdr — for the mmsg calls the array, whose first entry
     starts with one — and the exit, behind the stream check,
     reads its `msg_name`/`msg_namelen` (16 bytes, no loop) and
     goes on as for sendto. The hooks are listed in
     `internal/generate/sockowner.go` (`sockAddrHooks`), rendered
     behind `filter()` like the TCP owner hooks.
   - **Signals** (`internal/c/signal.c`): a raw `signal_generate`
     handler — the classic tracepoint names only the target's tid,
     the raw one its `task_struct` — emits a `signal_event` when
     the sender or the target (by tgid) is traced. The current task
     is the sender only of a task-sent signal: siginfo
     `SEND_SIG_NOINFO`, or `si_code` `SI_USER`, `SI_QUEUE` or
     `SI_TKILL`; `SEND_SIG_PRIV` and every other code (timers,
     `SIGCHLD`, `SIGIO`, faults) is the kernel's, with no sender
     (root test `TestKernelSignalHasNoSender`: a child's `SIGCHLD`
     names no traced sender). It
     fires inside kill(2), so the correlator attaches target and
     signal to the pending `kill`, and records a `signal` event on
     a traced receiver (args.sender_pid) unless the signal was
     ignored or not queued; both traced: a `signaled` edge.
   - **Correlator** (`internal/correlate/ipc.go`, `signals.go`):
     non-TCP connections are `sockState`s too, so their events wait
     for a conn id and draw `connected` edges like TCP's. A pipe or
     socketpair creates both fd instances (event fd = first end,
     `args.fd2`/`fd2_gen` the second) naming one connection, fused
     at once; a child that inherits an end, or dup2s it onto its
     stdio, joins the node. A unix stream connection's two ends
     share the sorted (sk, peer) key (`unixTable`). UDP is
     connectionless, so the unit is the *flow*: one (local,
     remote) pair, keyed per datagram; the two sides of a loopback
     flow mirror each other and fuse in a table of their own
     (`datagram`), like TCP's ends. A socket bound to the wildcard
     address sends from what the route picks, which towards a
     loopback peer is the peer's own address — anything else stays
     unresolved, never guessed; the wildcard itself is written as
     no `local_addr`. A flow ends with the fd that carried its first
     datagram (`dropFlows`), so the table stays bounded by open
     sockets; one left by an exec or exit lasts the session.
     epoll_ctl reports the epoll fd, the
     registered one as `args.fd2` and the op as `args.cmd`; the
     waits name the epoll fd and their latency is the block.
     Zero-copy calls report their first fd and the bytes moved
     (the second fd is not captured yet).
   - **Normalizer and goldens**: `args.fd2` is renumbered as an fd
     instance of its process, `target_pid`/`sender_pid` take their
     process's PID ordinal; expectations gain `args.fd2` (an `$fd`
     symbol) and `args.signal`; prune keeps a `signaled` edge when
     both processes have bracketed events.
   Scenarios (landed ahead, task na4; strace-verified on JDK 25,
   26 and 27-ea), now declared in full: `pipe-ipc` (each pipe2
   binds both ends — the JDK makes the child's stdin pipe before
   its stdout pipe — and three fused pipe connections, two shared
   with the child), `unix-socket` (path on bind and connect, one
   fused connection), `signals` (kill with signal 15, then 9; each
   child's `signal` event; two `signaled` edges), `udp-loopback`
   (per-datagram addresses, one fused flow) and
   `tcp-ipv4`/`tcp-ipv6` (full 4-tuples, fused). Integration
   tests (`integrationtests/ipc_test.go`): `TestPipeIPC` and
   `TestSignals` run with `-trace-families ipc` and `signal`,
   the others on the defaults. The first NIO use also makes a
   `socketpair` and throwaway AF_INET6 probe sockets; nothing
   declares them. A workload exiting with the skip code 3
   (`tcp-ipv6` without an IPv6 loopback) ends the harness run with
   `ErrWorkloadSkipped`, which `traceJudged` turns into `t.Skip`
   (`TestSkipCodeIsSkip`) — and `mage integrationTest`'s gate,
   which fails on any skip, says why.

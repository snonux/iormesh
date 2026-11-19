// Adapted from ior internal/c/flags.h @ b21afc50328c.
// Changes: PID_FILTER removed; the target set is target_map (maps.h);
// IORMESH_EXEC_WATCH added (M2 pattern sessions); IORMESH_RW_AGGREGATE added
// (M2 step 11); IORMESH_NATIVE_STACKS added (M2 step 9); IORMESH_JAVA_MARK
// added (javamark.c, task td4).

//+build ignore

const volatile u32 IOR_PID_FILTER = -1;
const volatile u32 TID_FILTER = -1;
// The traced processes are not a constant here but target_map (maps.h),
// which userspace fills.
//
// TID_FILTER_TGID is the thread group owning the TID_FILTER thread (resolved
// by userspace at setup, -1 when no tid filter or unresolvable). It scopes the
// group-dead sched_process_exit bypass of the tid filter to that one process;
// see ior_process_exit_in_scope in exec.c.
const volatile u32 TID_FILTER_TGID = -1;
// IOR_FILE_IDENT switches the file-identity capture on (1) or off (0); see
// fileident.c. It is off in the object and switched on by userspace before
// the load, which is also how userspace knows that the file_ident words of
// fd_event and ret_event are written at all: an object built before the
// capture has no such symbol (and leaves those bytes as stale padding).
const volatile u32 IOR_FILE_IDENT = 0;
// IORMESH_EXEC_WATCH (1) makes the sched_process_exec handler report every
// exec of an untraced process with its argv and mark it PENDING in
// target_map, so a pattern session can match it (exec.c, maps.h). PID and
// listening-port sessions leave it 0 and pay nothing per foreign exec.
const volatile u32 IORMESH_EXEC_WATCH = 0;
// IORMESH_RW_AGGREGATE (1) folds hot read/write loops in the kernel
// (rwagg.c, decision #25); 0 sends every call as its own enter and exit
// records, for debugging (iormesh run -aggregate=false). Userspace always
// sets it before the load; as a load-time constant the verifier prunes the
// disabled side, so an off switch costs nothing per call.
const volatile u32 IORMESH_RW_AGGREGATE = 1;
// IORMESH_NATIVE_STACKS (1) makes every emitted syscall enter send its user
// stack id first (stack.c; iormesh run -native-stacks, M2 step 9). Off by
// default (decision #25: a stack walk per call is not free), and like the
// switches above a load-time constant, so the verifier prunes the walk from
// every handler and an off switch costs nothing per call.
const volatile u32 IORMESH_NATIVE_STACKS = 0;
// IORMESH_JAVA_MARK (1) makes every emitted syscall enter of a thread inside
// a hooked JDK native send that native's mark first (javamark.c; iormesh run
// -spool, M3, task td4). Off without -spool: a load-time constant like the
// switches above, so the verifier prunes the per-enter map lookup from every
// handler and a session without the Java join pays nothing per call.
const volatile u32 IORMESH_JAVA_MARK = 0;

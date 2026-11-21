// Package javaattach live-loads the iormesh Java agent into a running JVM
// (decision #2's "trace an existing PID" path, task ge4). It speaks
// HotSpot's attach protocol itself instead of running a JDK's
// VirtualMachine.attach or jcmd: the tracer host needs no JDK, the
// tracer keeps every step under its own timeout, and it can refuse the
// steps that would hurt a target before taking them.
//
// The handshake, as jdk.attach's Linux VirtualMachineImpl does it:
//
//  1. If the JVM's attach socket /tmp/.java_pid<nspid> (in the JVM's own
//     mount namespace, reached through /proc/<pid>/root) is missing,
//     create the trigger file .attach_pid<nspid> in the JVM's working
//     directory (or its /tmp), send SIGQUIT, and wait for the socket.
//  2. Connect to the socket — a client connection: the tracer listens on
//     nothing — and send protocol 1's "load instrument false
//     <jar>=<options>" request.
//  3. Read the reply: a completion status line, then the load's result
//     ("return code: 0", or the JVM's message, such as JEP 451's
//     "Dynamic agent loading is not enabled").
//
// Safety, since the target is somebody's production JVM: SIGQUIT's
// default action kills a process, so it is sent only to a process that
// maps libjvm.so and has a SIGQUIT handler installed (SigCgt) — not to a
// JVM still starting, nor to one run with -Xrs — and through a pidfd, so
// a recycled pid is never signalled. It is sent at most once and only
// when the socket is missing: an initialized listener answers SIGQUIT
// with a thread dump on the target's stdout. A JVM started with
// -XX:+DisableAttachMechanism on its command line or in JAVA_TOOL_OPTIONS
// is refused for the same reason. Every wait is bounded by the caller's
// context; giving up never touches the target again, though a load the
// JVM already started then still completes inside it.
//
// Safety, since the tracer is root and the target owns every path of the
// handshake (a confused deputy otherwise): the JVM's working directory
// and /tmp are held as directory fds, /tmp opened with openat2's
// RESOLVE_IN_ROOT and no symlinks, and every file in them is created,
// checked and removed relative to those fds (dirs.go). The request is
// sent only after SO_PEERCRED showed the connected peer is the target's
// pid with its euid (checkPeer), whatever the socket's path led to.
//
// The jar and spool paths of the request are resolved by the JVM in its
// own mount namespace. For a JVM in another one (an app container beside
// an in-pod tracer, M7), Attacher.Stage stages a copy of the jar and a
// spool inside the JVM's root, under its /tmp, by the same fd discipline
// (stage.go, decision #14).
//
// The JVM checks the attacher's credentials itself: the peer's effective
// uid (and gid) must be its own, or root. A root tracer therefore
// reaches every JVM on the host (S1, Experiment 4); a non-root caller
// only its own uid's.
package javaattach

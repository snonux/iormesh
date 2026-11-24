package schema

// The M2 step 7 kinds (PLAN.md M2 "Full mental-model scope", decision #7):
// the socket family's address calls, which are on by default like the rest
// of the socket family, and the opt-in families — ipc, signal, poll and
// zerocopy (`iormesh run -trace-families`). Each is its kernel
// tracepoint's syscall name, as for every syscall kind, except KindSignal,
// which is a delivery. Appended, never renamed.
//
// Args per kind (docs/schema.md "Event kinds (M2 step 7)"):
//   - bind, connect: AF_UNIX args.path (the sun_path; an abstract name as
//     "@name"); inet args carry the local or remote address.
//   - sendto, recvfrom: bytes, and per datagram the local and remote
//     address and port (UDP's peer is in each call, not in the socket).
//   - sendmsg, recvmsg, sendmmsg, recvmmsg (task oh4): bytes; on a
//     connected socket the connection's 4-tuple, as read and write. The
//     mmsg calls' ret is the message count and bytes the sum of the
//     messages' msg_len; args.bytes_partial marks a sum the tracer could
//     not complete. A datagram's peer inside the msghdr is not read yet.
//     recvfrom, recvmsg and recvmmsg called with MSG_PEEK are marked
//     args.peek (task ok4): their bytes stay out of the stream sums.
//   - pipe, pipe2, socketpair: fd is the first descriptor, args.fd2 and
//     args.fd2_gen the second; both name one connection (conn_id).
//   - kill, tgkill, tkill: args.target_pid and args.signal, when the
//     kernel generated the signal (signal_generate).
//   - signal: a signal queued for a traced process; pid/tid are the
//     receiver, args.sender_pid the sender's tgid, args.signal its number.
//   - epoll_ctl: fd is the epoll fd, args.fd2 the registered one and
//     args.cmd the op; the waits' fd is the epoll fd, latency the block.
//   - sendfile64, splice, tee, copy_file_range: fd is the first
//     descriptor argument, bytes what moved.
const (
	// Socket family addresses (default-on).
	KindBind     = "bind"
	KindSendto   = "sendto"
	KindRecvfrom = "recvfrom"
	// Socket family msghdr data calls (default-on, task oh4).
	KindSendmsg  = "sendmsg"
	KindRecvmsg  = "recvmsg"
	KindSendmmsg = "sendmmsg"
	KindRecvmmsg = "recvmmsg"

	// ipc family: both ends of a pipe or socketpair.
	KindPipe       = "pipe"
	KindPipe2      = "pipe2"
	KindSocketpair = "socketpair"

	// signal family.
	KindKill   = "kill"
	KindTgkill = "tgkill"
	KindTkill  = "tkill"
	// KindSignal is a signal generated for a traced process
	// (signal:signal_generate), recorded on the receiver with its sender:
	// "who touched the process" (PLAN.md "What we trace", Signals).
	KindSignal = "signal"

	// poll family.
	KindEpollCreate  = "epoll_create"
	KindEpollCreate1 = "epoll_create1"
	KindEpollCtl     = "epoll_ctl"
	KindEpollWait    = "epoll_wait"
	KindEpollPwait   = "epoll_pwait"
	KindEpollPwait2  = "epoll_pwait2"
	KindPoll         = "poll"
	KindPpoll        = "ppoll"

	// zerocopy family.
	KindSendfile64    = "sendfile64"
	KindSplice        = "splice"
	KindTee           = "tee"
	KindCopyFileRange = "copy_file_range"
)

// ipcKind reports whether kind is one of the M2 step 7 kinds.
func ipcKind(kind string) bool {
	switch kind {
	case KindBind, KindSendto, KindRecvfrom,
		KindSendmsg, KindRecvmsg, KindSendmmsg, KindRecvmmsg,
		KindPipe, KindPipe2, KindSocketpair,
		KindKill, KindTgkill, KindTkill, KindSignal,
		KindEpollCreate, KindEpollCreate1, KindEpollCtl, KindEpollWait,
		KindEpollPwait, KindEpollPwait2, KindPoll, KindPpoll,
		KindSendfile64, KindSplice, KindTee, KindCopyFileRange:
		return true
	}
	return false
}

// signalKind reports whether kind may carry args.signal: the senders and
// the delivery.
func signalKind(kind string) bool {
	switch kind {
	case KindKill, KindTgkill, KindTkill, KindSignal:
		return true
	}
	return false
}

// IsMmsg reports whether kind is one of the multi-message calls, whose
// ret counts messages, not bytes (task oh4): bytes is the sum of their
// msg_len, which the tracer reads at the exit.
func IsMmsg(kind string) bool {
	return kind == KindSendmmsg || kind == KindRecvmmsg
}

// fd2Kind reports whether kind may carry args.fd2: the pair creators and
// epoll_ctl's registered fd.
func fd2Kind(kind string) bool {
	switch kind {
	case KindPipe, KindPipe2, KindSocketpair, KindEpollCtl:
		return true
	}
	return false
}

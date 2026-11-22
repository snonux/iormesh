package generate

import (
	"fmt"
	"strings"
)

// tcpSeqDirs names the data calls whose records may carry args.tcp_seq
// (M4 "Clock reconciliation", task xe4; schema.IsStreamSend and
// IsStreamReceive) and the tcpseq.c direction of each: a send's first byte
// is the socket's write_seq, a receive's its copied_seq. All of them take
// the socket as their first argument (sendfile64's out_fd). The msghdr
// calls joined in task oh4.
var tcpSeqDirs = map[string]string{
	"write":      "IORMESH_SEQ_SEND",
	"writev":     "IORMESH_SEQ_SEND",
	"sendto":     "IORMESH_SEQ_SEND",
	"sendfile64": "IORMESH_SEQ_SEND",
	"sendmsg":    "IORMESH_SEQ_SEND",
	"sendmmsg":   "IORMESH_SEQ_SEND",
	"read":       "IORMESH_SEQ_RECV",
	"readv":      "IORMESH_SEQ_RECV",
	"recvfrom":   "IORMESH_SEQ_RECV",
	"recvmsg":    "IORMESH_SEQ_RECV",
	"recvmmsg":   "IORMESH_SEQ_RECV",
}

// mmsgSyscalls are the data calls whose return counts messages, not bytes
// (internal/c/mmsg.c, task oh4): their enter also parks the mmsghdr array
// (the second argument), and their exit goes through mmsg.c's hook, which
// sums the messages' msg_len and checks tcp_seq against that sum.
var mmsgSyscalls = map[string]bool{"sendmmsg": true, "recvmmsg": true}

// renderTCPSeqHook writes the tcpseq.c hook of a data call's handler, if it
// has one: the enter parks the socket's sequence field; the exit checks it
// and, when it holds, sends the tcp_seq_event as the call's exit record
// and returns before the handler's own ret_event. Both go behind the
// per-tid hook (and, for read and write, the fold hook), which return
// early for a call whose record is not sent: a sampled-out or folded call
// must not pay for the walk, and a folded one cannot carry tcp_seq anyway.
// The mmsg calls' enter adds the array's park, and their exit is mmsg.c's
// hook, which sends an mmsg_event for every call that moved messages.
func renderTCPSeqHook(b *strings.Builder, h handlerSpec) {
	name := syscallName(h.name)
	dir, ok := tcpSeqDirs[name]
	if !ok {
		return
	}
	if h.isEnter {
		fmt.Fprintf(b, "    iormesh_seq_on_enter(tid, (__s32)ctx->args[0], %s);\n", dir)
		if mmsgSyscalls[name] {
			fmt.Fprintf(b, "    iormesh_mmsg_on_enter(tid, ctx->args[1], %s);\n", dir)
		}
		return
	}
	hook := "iormesh_seq_on_exit"
	if mmsgSyscalls[name] {
		hook = "iormesh_mmsg_on_exit"
	}
	fmt.Fprintf(b, "    if (%s(pid, tid, %s, now, ctx->ret, %s))\n", hook, strings.ToUpper(h.name), dir)
	b.WriteString("        return 0;\n")
}

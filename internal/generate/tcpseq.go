package generate

import (
	"fmt"
	"strings"
)

// tcpSeqDirs names the data calls whose records may carry args.tcp_seq
// (M4 "Clock reconciliation", task xe4; schema.IsStreamSend and
// IsStreamReceive) and the tcpseq.c direction of each: a send's first byte
// is the socket's write_seq, a receive's its copied_seq. All seven take the
// socket as their first argument (sendfile64's out_fd).
var tcpSeqDirs = map[string]string{
	"write":      "IORMESH_SEQ_SEND",
	"writev":     "IORMESH_SEQ_SEND",
	"sendto":     "IORMESH_SEQ_SEND",
	"sendfile64": "IORMESH_SEQ_SEND",
	"read":       "IORMESH_SEQ_RECV",
	"readv":      "IORMESH_SEQ_RECV",
	"recvfrom":   "IORMESH_SEQ_RECV",
}

// renderTCPSeqHook writes the tcpseq.c hook of a data call's handler, if it
// has one: the enter parks the socket's sequence field; the exit checks it
// and, when it holds, sends the tcp_seq_event as the call's exit record
// and returns before the handler's own ret_event. Both go behind the
// per-tid hook (and, for read and write, the fold hook), which return
// early for a call whose record is not sent: a sampled-out or folded call
// must not pay for the walk, and a folded one cannot carry tcp_seq anyway.
func renderTCPSeqHook(b *strings.Builder, h handlerSpec) {
	dir, ok := tcpSeqDirs[syscallName(h.name)]
	if !ok {
		return
	}
	if h.isEnter {
		fmt.Fprintf(b, "    iormesh_seq_on_enter(tid, (__s32)ctx->args[0], %s);\n", dir)
		return
	}
	fmt.Fprintf(b, "    if (iormesh_seq_on_exit(pid, tid, %s, now, ctx->ret, %s))\n",
		strings.ToUpper(h.name), dir)
	b.WriteString("        return 0;\n")
}

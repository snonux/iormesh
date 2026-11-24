package generate

import (
	"fmt"
	"strings"
)

// sockOwnerHooks names, per tracepoint, the sock.c hook that registers or
// unregisters a TCP socket with its owning process (internal/c/sock.c,
// iormesh M1 step 4): connect at enter - the socket must be known before the
// kernel's own state transitions inside connect fire - and at exit, which
// drops a socket the connect left in TCP_CLOSE without any transition; the
// two accepts at exit, where the return value is the accepted descriptor.
// Every other handler has none.
var sockOwnerHooks = map[string]string{
	"sys_enter_connect": "iormesh_sock_on_connect(pid, tid, (__s32)ctx->args[0])",
	"sys_exit_connect":  "iormesh_sock_on_connect_exit(tid)",
	"sys_exit_accept":   "iormesh_sock_on_accept(pid, ctx->ret)",
	"sys_exit_accept4":  "iormesh_sock_on_accept(pid, ctx->ret)",
}

// sockAddrHooks names, per tracepoint, the sockaddr.c hook that reports a
// socket call's addresses (internal/c/sockaddr.c, M2 step 7): the enter of
// bind, connect, sendto and recvfrom parks the address argument and its
// length (recvfrom's is a pointer the kernel writes back), their exit sends
// it with the socket's own addresses right before the exit record; the
// accept exits report an accepted AF_UNIX socket and its peer. bind and
// connect report failures too, connect never a TCP socket, sendto and
// recvfrom never a stream socket - send(3)/recv(3) on a connection are
// these syscalls (IORMESH_ADDR_* in sockaddr.c). The msghdr data calls
// report a datagram's peer the same way, from their msg_name (task nk4).
var sockAddrHooks = map[string]string{
	"sys_enter_bind":     "iormesh_addr_on_enter(tid, (__s32)ctx->args[0], ctx->args[1], ctx->args[2], 0)",
	"sys_enter_connect":  "iormesh_addr_on_enter(tid, (__s32)ctx->args[0], ctx->args[1], ctx->args[2], 0)",
	"sys_enter_sendto":   "iormesh_addr_on_enter(tid, (__s32)ctx->args[0], ctx->args[4], ctx->args[5], 0)",
	"sys_enter_recvfrom": "iormesh_addr_on_enter(tid, (__s32)ctx->args[0], ctx->args[4], ctx->args[5], 1)",
	"sys_exit_bind": "iormesh_addr_on_exit(pid, tid, SYS_EXIT_BIND, ctx->ret, " +
		"IORMESH_ADDR_FAILED_TOO)",
	"sys_exit_connect": "iormesh_addr_on_exit(pid, tid, SYS_EXIT_CONNECT, ctx->ret, " +
		"IORMESH_ADDR_FAILED_TOO | IORMESH_ADDR_NOT_TCP)",
	"sys_exit_sendto": "iormesh_addr_on_exit(pid, tid, SYS_EXIT_SENDTO, ctx->ret, " +
		"IORMESH_ADDR_NOT_STREAM)",
	"sys_exit_recvfrom": "iormesh_addr_on_exit(pid, tid, SYS_EXIT_RECVFROM, ctx->ret, " +
		"IORMESH_ADDR_NOT_STREAM)",
	"sys_exit_accept":  "iormesh_addr_on_accept(pid, tid, SYS_EXIT_ACCEPT, ctx->ret)",
	"sys_exit_accept4": "iormesh_addr_on_accept(pid, tid, SYS_EXIT_ACCEPT4, ctx->ret)",
	// The msghdr calls (task nk4) park the msghdr (the mmsg calls: their
	// array, whose first entry starts with one) and read its msg_name at
	// the exit; a stream socket sends nothing, as for sendto.
	"sys_enter_sendmsg":  "iormesh_addr_on_enter(tid, (__s32)ctx->args[0], ctx->args[1], 0, 0)",
	"sys_enter_recvmsg":  "iormesh_addr_on_enter(tid, (__s32)ctx->args[0], ctx->args[1], 0, 0)",
	"sys_enter_sendmmsg": "iormesh_addr_on_enter(tid, (__s32)ctx->args[0], ctx->args[1], 0, 0)",
	"sys_enter_recvmmsg": "iormesh_addr_on_enter(tid, (__s32)ctx->args[0], ctx->args[1], 0, 0)",
	"sys_exit_sendmsg": "iormesh_addr_on_exit(pid, tid, SYS_EXIT_SENDMSG, ctx->ret, " +
		"IORMESH_ADDR_NOT_STREAM | IORMESH_ADDR_MSGHDR)",
	"sys_exit_recvmsg": "iormesh_addr_on_exit(pid, tid, SYS_EXIT_RECVMSG, ctx->ret, " +
		"IORMESH_ADDR_NOT_STREAM | IORMESH_ADDR_MSGHDR)",
	"sys_exit_sendmmsg": "iormesh_addr_on_exit(pid, tid, SYS_EXIT_SENDMMSG, ctx->ret, " +
		"IORMESH_ADDR_NOT_STREAM | IORMESH_ADDR_MSGHDR)",
	"sys_exit_recvmmsg": "iormesh_addr_on_exit(pid, tid, SYS_EXIT_RECVMMSG, ctx->ret, " +
		"IORMESH_ADDR_NOT_STREAM | IORMESH_ADDR_MSGHDR)",
}

// renderSockOwnerHook writes the socket (un)registration call of the handler
// named name, if it has one, and then its address hook (sockAddrHooks). It goes right behind filter() and ahead of the
// per-tid hook on purpose: that hook returns early when sampling sheds the
// syscall's own record, but the registration decides whether the
// connection's 4-tuple is reported at all, and the connect exit's cleanup
// whether a dead socket's address stays registered, so neither may depend
// on the sampling rate.
// The address hooks sit in the same place for the same reason: whether a
// datagram's peer is reported must not depend on sampling either.
func renderSockOwnerHook(b *strings.Builder, name string) {
	owner, hasOwner := sockOwnerHooks[name]
	addr, hasAddr := sockAddrHooks[name]
	if !hasOwner && !hasAddr {
		return
	}
	if hasOwner {
		fmt.Fprintf(b, "    %s;\n", owner)
	}
	if hasAddr {
		fmt.Fprintf(b, "    %s;\n", addr)
	}
	b.WriteString("\n")
}

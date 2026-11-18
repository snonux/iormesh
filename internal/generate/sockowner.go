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

// renderSockOwnerHook writes the socket (un)registration call of the handler
// named name, if it has one. It goes right behind filter() and ahead of the
// per-tid hook on purpose: that hook returns early when sampling sheds the
// syscall's own record, but the registration decides whether the
// connection's 4-tuple is reported at all, and the connect exit's cleanup
// whether a dead socket's address stays registered, so neither may depend
// on the sampling rate.
func renderSockOwnerHook(b *strings.Builder, name string) {
	hook, ok := sockOwnerHooks[name]
	if !ok {
		return
	}
	fmt.Fprintf(b, "    %s;\n", hook)
	b.WriteString("\n")
}

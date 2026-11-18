package generate

import (
	"strings"
	"testing"
)

// TestSockOwnerHooksInCommittedHandlers pins where the committed artifact
// registers and unregisters TCP sockets (internal/c/sock.c): exactly in the
// connect enter and exit and the two accept exits, once each, and behind filter() but ahead of the
// per-tid hook - a sampled-out call returns from that hook, and its socket
// must still be registered or the connection's 4-tuple is never reported.
func TestSockOwnerHooksInCommittedHandlers(t *testing.T) {
	artifact, err := readGeneratedTracepointsC()
	if err != nil {
		t.Fatalf("read generated tracepoints C: %v", err)
	}
	if got := strings.Count(artifact, "iormesh_sock_on_"); got != len(sockOwnerHooks) {
		t.Errorf("artifact calls the sock hooks %d times, want %d (one per sockOwnerHooks entry)",
			got, len(sockOwnerHooks))
	}
	for name, hook := range sockOwnerHooks {
		body := handlerBody(t, artifact, name)
		hookAt := strings.Index(body, hook+";")
		filterAt := strings.Index(body, "if (filter(&pid, &tid))")
		syscallHookAt := strings.Index(body, "if (!ior_on_syscall_")
		if hookAt < 0 || filterAt < 0 || syscallHookAt < 0 {
			t.Errorf("%s: hook=%d filter=%d per-tid hook=%d, want all present:\n%s",
				name, hookAt, filterAt, syscallHookAt, body)
			continue
		}
		if !(filterAt < hookAt && hookAt < syscallHookAt) {
			t.Errorf("%s: sock hook not between filter() and the per-tid hook:\n%s", name, body)
		}
	}
}

// TestSockOwnerHookOnlyForItsHandlers: the enter sides of accept and an
// unrelated syscall render no socket registration.
func TestSockOwnerHookOnlyForItsHandlers(t *testing.T) {
	for _, name := range []string{"sys_enter_accept", "sys_enter_accept4", "sys_enter_read", "sys_exit_write"} {
		var b strings.Builder
		renderSockOwnerHook(&b, name)
		if b.Len() != 0 {
			t.Errorf("%s renders %q, want nothing", name, b.String())
		}
	}
}

// TestSockAddrHooksInCommittedHandlers pins the address hooks of M2 step 7
// (internal/c/sockaddr.c) the same way: once each, behind filter() and
// ahead of the per-tid hook, so a sampled-out call still parks and reports
// its address, and the exit's report precedes the exit record.
func TestSockAddrHooksInCommittedHandlers(t *testing.T) {
	artifact, err := readGeneratedTracepointsC()
	if err != nil {
		t.Fatalf("read generated tracepoints C: %v", err)
	}
	if got := strings.Count(artifact, "iormesh_addr_on_"); got != len(sockAddrHooks) {
		t.Errorf("artifact calls the address hooks %d times, want %d", got, len(sockAddrHooks))
	}
	for name, hook := range sockAddrHooks {
		body := handlerBody(t, artifact, name)
		hookAt := strings.Index(body, hook+";")
		filterAt := strings.Index(body, "if (filter(&pid, &tid))")
		syscallHookAt := strings.Index(body, "if (!ior_on_syscall_")
		if hookAt < 0 || !(filterAt < hookAt && hookAt < syscallHookAt) {
			t.Errorf("%s: address hook missing or misplaced:\n%s", name, body)
		}
	}
}

// TestConnectRendersBothHooks: connect's enter registers its TCP socket
// first and then parks its address; neither replaces the other.
func TestConnectRendersBothHooks(t *testing.T) {
	var b strings.Builder
	renderSockOwnerHook(&b, "sys_enter_connect")
	got := b.String()
	owner := strings.Index(got, sockOwnerHooks["sys_enter_connect"])
	addr := strings.Index(got, sockAddrHooks["sys_enter_connect"])
	if owner < 0 || addr < 0 || owner > addr {
		t.Errorf("connect enter renders %q, want the owner hook, then the address hook", got)
	}
}

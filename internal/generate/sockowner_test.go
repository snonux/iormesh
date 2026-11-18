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
	for _, name := range []string{"sys_enter_accept", "sys_enter_accept4", "sys_enter_read"} {
		var b strings.Builder
		renderSockOwnerHook(&b, name)
		if b.Len() != 0 {
			t.Errorf("%s renders %q, want nothing", name, b.String())
		}
	}
}

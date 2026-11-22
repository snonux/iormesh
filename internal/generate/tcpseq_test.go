package generate

import (
	"strings"
	"testing"
)

// TestTCPSeqHooksInCommittedHandlers pins where the committed artifact
// calls the tcpseq.c hooks (task xe4): once in each enter and exit handler
// of the data calls and nowhere else, behind the per-tid hook - and for
// read and write the fold hook - which return early for a record that is
// not sent, and ahead of the handler's own reserve; an exit that sent its
// tcp_seq_event returns before the ret_event. The mmsg calls' exits call
// mmsg.c's hook in place of tcpseq.c's (task oh4).
func TestTCPSeqHooksInCommittedHandlers(t *testing.T) {
	artifact, err := readGeneratedTracepointsC()
	if err != nil {
		t.Fatalf("read generated tracepoints C: %v", err)
	}
	want := 2*len(tcpSeqDirs) - len(mmsgSyscalls)
	if got := strings.Count(artifact, "iormesh_seq_on_"); got != want {
		t.Errorf("artifact calls the tcp_seq hooks %d times, want %d", got, want)
	}
	for syscall, dir := range tcpSeqDirs {
		for _, name := range []string{"sys_enter_" + syscall, "sys_exit_" + syscall} {
			checkSeqHookPlacement(t, handlerBody(t, artifact, name), name, seqExitHook(syscall), dir)
		}
	}
}

// seqExitHook is the exit hook syscall's handler calls.
func seqExitHook(syscall string) string {
	if mmsgSyscalls[syscall] {
		return "iormesh_mmsg_on_exit("
	}
	return "iormesh_seq_on_exit("
}

// checkSeqHookPlacement requires the hook of handler name, an enter's
// iormesh_seq_on_enter or the exit's exitHook, between its last gate and
// its reserve, passing dir; an exit must act on the hook's verdict.
func checkSeqHookPlacement(t *testing.T, body, name, exitHook, dir string) {
	t.Helper()
	hook := "iormesh_seq_on_enter("
	if strings.HasPrefix(name, "sys_exit_") {
		hook = exitHook
	}
	hookAt := strings.Index(body, hook)
	gateAt := lastGate(body)
	reserveAt := strings.Index(body, "bpf_ringbuf_reserve(&event_map")
	if hookAt < 0 || gateAt < 0 || !(gateAt < hookAt && hookAt < reserveAt) {
		t.Errorf("%s: tcp_seq hook=%d gate=%d reserve=%d, want gate < hook < reserve:\n%s",
			name, hookAt, gateAt, reserveAt, body)
	}
	if !strings.Contains(body, dir+")") {
		t.Errorf("%s: hook does not pass %s:\n%s", name, dir, body)
	}
	if strings.HasPrefix(name, "sys_exit_") && !strings.Contains(body, "if ("+exitHook) {
		t.Errorf("%s: exit hook's verdict is not acted on:\n%s", name, body)
	}
}

// TestMmsgHooksInCommittedHandlers: the enters of sendmmsg and recvmmsg
// park their mmsghdr array (the second argument) right after the
// sequence park, and no other handler calls mmsg.c (task oh4).
func TestMmsgHooksInCommittedHandlers(t *testing.T) {
	artifact, err := readGeneratedTracepointsC()
	if err != nil {
		t.Fatalf("read generated tracepoints C: %v", err)
	}
	if got := strings.Count(artifact, "iormesh_mmsg_on_"); got != 2*len(mmsgSyscalls) {
		t.Errorf("artifact calls the mmsg hooks %d times, want %d", got, 2*len(mmsgSyscalls))
	}
	for syscall := range mmsgSyscalls {
		body := handlerBody(t, artifact, "sys_enter_"+syscall)
		seqAt := strings.Index(body, "iormesh_seq_on_enter(")
		parkAt := strings.Index(body, "iormesh_mmsg_on_enter(tid, ctx->args[1], "+tcpSeqDirs[syscall]+");")
		if seqAt < 0 || parkAt < seqAt {
			t.Errorf("sys_enter_%s: seq park=%d mmsg park=%d, want the mmsg park after:\n%s",
				syscall, seqAt, parkAt, body)
		}
	}
}

// lastGate is the position of the last hook that may return before the
// record is sent: the fold hook of read and write, else the per-tid hook.
func lastGate(body string) int {
	if at := strings.Index(body, "if (!iormesh_rw_on_"); at >= 0 {
		return at
	}
	return strings.Index(body, "if (!ior_on_syscall_")
}

// TestTCPSeqHookOnlyForDataCalls: handlers of other syscalls render none.
func TestTCPSeqHookOnlyForDataCalls(t *testing.T) {
	for _, name := range []string{"sys_enter_pread64", "sys_exit_pwrite64", "sys_enter_getsockopt",
		"sys_exit_shutdown", "sys_enter_connect"} {
		var b strings.Builder
		renderTCPSeqHook(&b, handlerSpec{name: name, isEnter: strings.HasPrefix(name, "sys_enter_")})
		if b.Len() != 0 {
			t.Errorf("%s renders %q, want nothing", name, b.String())
		}
	}
}

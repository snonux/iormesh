package generate

import (
	"os"
	"strings"
	"testing"
)

// javaMarkHookCall is the line renderJavaMarkHook writes
// (internal/c/javamark.c).
const javaMarkHookCall = "    iormesh_java_mark_on_enter(pid, tid, now);\n"

// TestJavaMarkHookOnEveryEmittedEnter pins task td4's placement for every
// hook variant, as TestStackHookOnEveryEmittedEnter does for the stack
// hook: each enter handler calls the mark hook exactly once, after the
// clock read and every early return that drops the enter and before the
// record it sends, so a mark goes out only for an enter that does, right
// ahead of it — the pairing userspace relies on (tid and enter time).
// Exit handlers never call it.
func TestJavaMarkHookOnEveryEmittedEnter(t *testing.T) {
	pairs := []struct{ enter, exit string }{
		{FormatRead, FormatExitRead},
		{FormatOpenat, FormatExitOpenat},
		{FormatClose, FormatExitClose},
		{FormatExecve, FormatExitExecve},
		{FormatIoUringRegister, FormatExitIoUringRegister},
		{syntheticEnter("exit_group", 60), syntheticExit("exit_group", 59)},
	}
	var input strings.Builder
	for _, p := range pairs {
		input.WriteString(p.enter + "\n" + p.exit + "\n")
	}
	handlers := splitGeneratedHandlers(t, GenerateTracepointsC(mustParseAll(t, input.String())))
	for name, body := range handlers {
		if err := checkJavaMarkHook(name, body); err != "" {
			t.Error(err)
		}
	}
}

// TestHeldEnterCarriesMarkAndStack covers the one enter record the
// generated handlers do not send: the enter a call the read/write fold
// held owes once its exit shows it cannot fold (EOF, an error, a call
// slower than 1 ms; internal/c/rwagg.c, iormesh_rw_send_held_enter). The
// handler returned before its hooks when it held the call, so the owed
// enter must send the stack and the Java mark itself, stamped with the
// call's enter time and ahead of its record (task ti4) - else the last
// call of every hot Java read loop counts as JVM work. TestRwaggHarness
// checks the order at run time; this pins the arguments.
func TestHeldEnterCarriesMarkAndStack(t *testing.T) {
	src, err := readCSource("rwagg.c")
	if err != nil {
		t.Fatal(err)
	}
	const head = "static __always_inline void iormesh_rw_send_held_enter("
	start := strings.Index(src, head)
	if start < 0 {
		t.Fatal("iormesh_rw_send_held_enter not found in rwagg.c")
	}
	body := src[start:]
	body = body[:strings.Index(body, "\n}\n")]
	send := strings.Index(body, "bpf_ringbuf_reserve(")
	for _, hook := range []string{
		"    iormesh_stack_on_enter(ctx, pid, tid, enter_ns);\n",
		"    iormesh_java_mark_on_enter(pid, tid, enter_ns);\n",
	} {
		at := strings.Index(body, hook)
		switch {
		case strings.Count(body, hook) != 1:
			t.Errorf("held enter: want exactly one %q", strings.TrimSpace(hook))
		case send >= 0 && at > send:
			t.Errorf("held enter: %q after its record", strings.TrimSpace(hook))
		case strings.Contains(body[:at], "return"):
			t.Errorf("held enter: may return before %q", strings.TrimSpace(hook))
		}
	}
}

// checkJavaMarkHook returns what is wrong with one handler's mark hook.
func checkJavaMarkHook(name, body string) string {
	n := strings.Count(body, javaMarkHookCall)
	if !strings.HasPrefix(name, "sys_enter_") {
		if n != 0 {
			return name + ": exit handler calls the Java mark hook"
		}
		return ""
	}
	if n != 1 {
		return name + ": want exactly one Java mark hook call"
	}
	hook := strings.Index(body, javaMarkHookCall)
	clock := strings.Index(body, clockReadLine)
	send := firstIndex(body, "bpf_ringbuf_reserve(", "ior_emit_fd_name_enter(")
	switch {
	case clock < 0 || clock > hook:
		return name + ": Java mark hook before the clock read"
	case send < hook:
		return name + ": Java mark hook after the enter record was sent"
	case strings.Contains(body[hook:send], "return 0;"):
		return name + ": the enter may be dropped after its mark was sent"
	}
	return ""
}

// TestCommittedHandlersCallTheJavaMarkHook checks the committed
// generated_tracepoints.c (mage generate) against the generator: one mark
// hook call per enter handler, so a stale file cannot ship enters whose
// syscalls never carry their native.
func TestCommittedHandlersCallTheJavaMarkHook(t *testing.T) {
	data, err := os.ReadFile("../c/generated_tracepoints.c")
	if err != nil {
		t.Fatalf("read committed handlers: %v", err)
	}
	src := string(data)
	enters := strings.Count(src, "SEC(\"tracepoint/syscalls/sys_enter_")
	if calls := strings.Count(src, javaMarkHookCall); calls != enters || enters == 0 {
		t.Fatalf("%d Java mark hook calls for %d enter handlers; run mage generate", calls, enters)
	}
}

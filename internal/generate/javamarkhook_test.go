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

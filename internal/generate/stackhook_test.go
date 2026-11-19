package generate

import (
	"os"
	"strings"
	"testing"
)

// stackHookCall is the line renderStackHook writes (internal/c/stack.c).
const stackHookCall = "    iormesh_stack_on_enter(ctx, pid, tid, now);\n"

// TestStackHookOnEveryEmittedEnter pins M2 step 9's placement for every
// hook variant: each enter handler calls the stack hook exactly once,
// after the clock read and every early return that drops the enter (the
// sampling hook, read's fold) and before the record it sends — the plain
// reserve, or close's fd-name record — so a stack goes out only for an
// enter that does, right ahead of it. Exit handlers never call it.
func TestStackHookOnEveryEmittedEnter(t *testing.T) {
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
		if err := checkStackHook(name, body); err != "" {
			t.Error(err)
		}
	}
}

// checkStackHook returns what is wrong with one handler's stack hook.
func checkStackHook(name, body string) string {
	n := strings.Count(body, stackHookCall)
	if !strings.HasPrefix(name, "sys_enter_") {
		if n != 0 {
			return name + ": exit handler calls the stack hook"
		}
		return ""
	}
	if n != 1 {
		return name + ": want exactly one stack hook call"
	}
	hook := strings.Index(body, stackHookCall)
	clock := strings.Index(body, clockReadLine)
	send := firstIndex(body, "bpf_ringbuf_reserve(", "ior_emit_fd_name_enter(")
	switch {
	case clock < 0 || clock > hook:
		return name + ": stack hook before the clock read"
	case send < hook:
		return name + ": stack hook after the enter record was sent"
	case strings.Contains(body[hook:send], "return 0;"):
		return name + ": the enter may be dropped after its stack was sent"
	}
	return ""
}

// firstIndex returns the earliest index of any of subs in s, or len(s).
func firstIndex(s string, subs ...string) int {
	first := len(s)
	for _, sub := range subs {
		if i := strings.Index(s, sub); i >= 0 && i < first {
			first = i
		}
	}
	return first
}

// TestCommittedHandlersCallTheStackHook checks the committed
// generated_tracepoints.c (mage generate) against the generator: one stack
// hook call per enter handler, so a stale file cannot ship enters that
// never send their stack.
func TestCommittedHandlersCallTheStackHook(t *testing.T) {
	data, err := os.ReadFile("../c/generated_tracepoints.c")
	if err != nil {
		t.Fatalf("read committed handlers: %v", err)
	}
	src := string(data)
	enters := strings.Count(src, "SEC(\"tracepoint/syscalls/sys_enter_")
	if calls := strings.Count(src, stackHookCall); calls != enters || enters == 0 {
		t.Fatalf("%d stack hook calls for %d enter handlers; run mage generate", calls, enters)
	}
}

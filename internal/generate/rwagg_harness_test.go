package generate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The read/write aggregation (internal/c/rwagg.c, M2 step 11, decision
// #25) decides which calls of a hot loop are folded and when the folded
// sums reach the ring buffer. A wrong decision either loses calls, sends
// them twice, or puts them out of their thread's order, and none of that
// shows on a short integration scenario. As with the restart fold
// (restart_harness_test.go), the real rwagg.c is compiled with the host C
// compiler against a simulated map and ring buffer and driven through the
// call sequences that matter; each scenario is also run against mutated
// sources, so the suite is shown to catch the regressions it exists for.

// rwaggHarnessTemplate simulates what rwagg.c uses of BPF: rw_agg_map as a
// small table keyed by tid ("full 1" makes inserts fail, as a full map
// does), the ring buffer ("drop 1" makes every reserve fail) and the drop
// counter. Commands name the tid first; each prints one line: the records
// the step sent, in order - "agg(trace fd count bytes first-last lat)" for
// a folded record, "enter(trace fd time)" for a held call's owed enter,
// "lost" for a failed reserve - then the hook's verdict for enter ("send" or
// "hold") and exit ("send" or "fold"), or "-" when nothing happened.
//
// "enter T trace fd now" and "exit T trace ret now" call the generated
// handlers' hooks; "flush T" and "forget T" are the flush points filter.c,
// signal.c and exec.c call.
const rwaggHarnessTemplate = `#include <stdio.h>
#include <string.h>

typedef unsigned int __u32;
typedef int __s32;
typedef unsigned long long __u64;
typedef long long __s64;
#ifndef __always_inline
#define __always_inline inline __attribute__((always_inline))
#endif
#define BPF_NOEXIST 1
#define ENTER_FD_EVENT 5
#define RW_AGGREGATE_EVENT 73

%s

%s

#define SIM_THREADS 8
static struct { __u32 tid, used; struct iormesh_rw_state st; } sim_map[SIM_THREADS];
static int rw_agg_map, event_map;
static int sim_drop, sim_full;
static __u32 IORMESH_RW_AGGREGATE = 1;
static union { struct fd_event fd; struct rw_aggregate_event agg; } sim_record;
static char sim_out[512];

static void sim_print(const char *s) {
    if (sim_out[0])
        strncat(sim_out, " ", sizeof(sim_out) - strlen(sim_out) - 1);
    strncat(sim_out, s, sizeof(sim_out) - strlen(sim_out) - 1);
}

static void *bpf_map_lookup_elem(void *map, const void *key) {
    (void)map;
    for (int i = 0; i < SIM_THREADS; i++)
        if (sim_map[i].used && sim_map[i].tid == *(const __u32 *)key)
            return &sim_map[i].st;
    return NULL;
}

static long bpf_map_update_elem(void *map, const void *key, const void *value, __u64 flags) {
    if (flags != BPF_NOEXIST || bpf_map_lookup_elem(map, key) || sim_full)
        return -1;
    for (int i = 0; i < SIM_THREADS; i++) {
        if (!sim_map[i].used) {
            sim_map[i].used = 1;
            sim_map[i].tid = *(const __u32 *)key;
            memcpy(&sim_map[i].st, value, sizeof(sim_map[i].st));
            return 0;
        }
    }
    return -1;
}

static long bpf_map_delete_elem(void *map, const void *key) {
    (void)map;
    for (int i = 0; i < SIM_THREADS; i++)
        if (sim_map[i].used && sim_map[i].tid == *(const __u32 *)key)
            sim_map[i].used = 0;
    return 0;
}

static void *bpf_ringbuf_reserve(void *map, __u64 size, __u64 flags) {
    (void)map;
    (void)flags;
    if (sim_drop || size > sizeof(sim_record))
        return NULL;
    memset(&sim_record, 0xA5, sizeof(sim_record));
    return &sim_record;
}

static void bpf_ringbuf_submit(void *data, __u64 flags) {
    char line[160];
    (void)flags;
    if (((struct fd_event *)data)->event_type == ENTER_FD_EVENT) {
        struct fd_event *ev = data;
        snprintf(line, sizeof(line), "enter(%%u %%d %%llu)", ev->trace_id, ev->fd, ev->time);
    } else {
        struct rw_aggregate_event *ev = data;
        if (ev->event_type != RW_AGGREGATE_EVENT || ev->reserved || ev->pid != 77)
            snprintf(line, sizeof(line), "malformed");
        else
            snprintf(line, sizeof(line), "agg(%%u %%d %%u %%llu %%llu-%%llu %%llu)", ev->trace_id, ev->fd,
                     ev->count, ev->total_bytes, ev->time, ev->end_time, ev->latency_ns);
    }
    sim_print(line);
}

static void ior_count_ringbuf_drop(void) {
    sim_print("lost");
}

%s

int main(void) {
    char cmd[16];
    unsigned long long tid, a, b, now;
    long long ret;
    while (scanf("%%15s %%llu", cmd, &tid) == 2) {
        sim_out[0] = 0;
        if (!strcmp(cmd, "drop")) {
            sim_drop = tid;
        } else if (!strcmp(cmd, "full")) {
            sim_full = tid;
        } else if (!strcmp(cmd, "enter") && scanf("%%llu %%llu %%llu", &a, &b, &now) == 3) {
            sim_print(iormesh_rw_on_enter(77, tid, a, (__s32)b, 0, now) ? "send" : "hold");
        } else if (!strcmp(cmd, "exit") && scanf("%%llu %%lld %%llu", &a, &ret, &now) == 3) {
            sim_print(iormesh_rw_on_exit(77, tid, a, ret, now) ? "send" : "fold");
        } else if (!strcmp(cmd, "flush")) {
            iormesh_rw_flush(tid);
        } else if (!strcmp(cmd, "forget")) {
            iormesh_rw_forget(tid);
            if (bpf_map_lookup_elem(&rw_agg_map, &(__u32){tid}))
                sim_print("kept");
        } else {
            printf("bad command %%s\n", cmd);
            return 1;
        }
        printf("%%s\n", sim_out[0] ? sim_out : "-");
    }
    return 0;
}
`

// rwaggStep is one harness command and the line it must print.
type rwaggStep struct {
	cmd  string
	want string
}

type rwaggScenario struct {
	name  string
	steps []rwaggStep
}

// warmup sends n hot write calls of 10 bytes on fd 3 of tid 9, starting at
// t (µs), 2 µs apart, each 1 µs long: the individual calls before folding.
func rwaggWarmup(n int, t int) []rwaggStep {
	var steps []rwaggStep
	for i := range n {
		at := (t + 2*i) * 1000
		steps = append(steps, rwaggStep{fmt.Sprintf("enter 9 2 3 %d", at), "send"},
			rwaggStep{fmt.Sprintf("exit 9 2 10 %d", at+1000), "send"})
	}
	return steps
}

// rwaggSteps joins step groups into one scenario's steps.
func rwaggSteps(groups ...[]rwaggStep) []rwaggStep {
	var steps []rwaggStep
	for _, g := range groups {
		steps = append(steps, g...)
	}
	return steps
}

var rwaggScenarios = []rwaggScenario{
	// The first four hot calls of a key are sent; the next ones are held
	// and folded, and a flush sends them as one record with their sums: the
	// first enter, the last exit, the summed latency.
	{name: "a hot loop folds after the individual calls", steps: rwaggSteps(rwaggWarmup(4, 0), []rwaggStep{
		{"enter 9 2 3 8000", "hold"}, {"exit 9 2 10 9000", "fold"},
		{"enter 9 2 3 10000", "hold"}, {"exit 9 2 7 11000", "fold"},
		{"flush 9", "agg(2 3 2 17 8000-11000 2000)"}, {"flush 9", "-"},
	})},
	// A flush frees the keys: the next calls start a new streak and are
	// sent again, so the first calls after any other syscall are exact.
	{name: "a flush restarts the streak", steps: rwaggSteps(rwaggWarmup(4, 0), []rwaggStep{
		{"enter 9 2 3 8000", "hold"}, {"exit 9 2 10 9000", "fold"},
		{"flush 9", "agg(2 3 1 10 8000-9000 1000)"},
	}, rwaggWarmup(4, 10), []rwaggStep{{"enter 9 2 3 18000", "hold"}})},
	// A held call that is not hot - EOF, an error, a slow call - sends the
	// key's folded calls, then its own enter (with its own time) and exit,
	// and its key starts over.
	{name: "an unfoldable held call is sent after the folded ones", steps: rwaggSteps(rwaggWarmup(4, 0), []rwaggStep{
		{"enter 9 2 3 8000", "hold"}, {"exit 9 2 10 9000", "fold"},
		{"enter 9 2 3 10000", "hold"}, {"exit 9 2 0 11000", "agg(2 3 1 10 8000-9000 1000) enter(2 3 10000) send"},
		{"enter 9 2 3 12000", "send"},
		{"exit 9 2 10 13000", "send"},
	}, []rwaggStep{
		{"enter 9 2 3 14000", "send"}, {"exit 9 2 -4 15000", "send"},
		{"enter 9 2 3 16000", "send"},
	})},
	{name: "a slow held call is sent, not folded", steps: rwaggSteps(rwaggWarmup(4, 0), []rwaggStep{
		{"enter 9 2 3 8000", "hold"}, {"exit 9 2 10 2000000", "enter(2 3 8000) send"},
	})},
	// A gap longer than the hot window ends the loop: the key's folded
	// calls are sent and the late call is sent itself, as a new streak.
	{name: "a gap ends the loop", steps: rwaggSteps(rwaggWarmup(4, 0), []rwaggStep{
		{"enter 9 2 3 8000", "hold"}, {"exit 9 2 10 9000", "fold"},
		{"enter 9 2 3 5000000", "agg(2 3 1 10 8000-9000 1000) send"},
	})},
	// Two keys - a write fd and a read fd, as in the syscall burst - fold
	// side by side; each flush sends both.
	{name: "two keys fold side by side", steps: rwaggSteps(rwaggWarmup(4, 0), []rwaggStep{
		{"enter 9 1 4 8100", "send"}, {"exit 9 1 1 8200", "send"},
		{"enter 9 1 4 8300", "send"}, {"exit 9 1 1 8400", "send"},
		{"enter 9 1 4 8500", "send"}, {"exit 9 1 1 8600", "send"},
		{"enter 9 1 4 8700", "send"}, {"exit 9 1 1 8800", "send"},
		{"enter 9 2 3 9000", "hold"}, {"exit 9 2 10 9100", "fold"},
		{"enter 9 1 4 9200", "hold"}, {"exit 9 1 1 9300", "fold"},
		{"enter 9 2 3 9400", "hold"}, {"exit 9 2 10 9500", "fold"},
		{"flush 9", "agg(2 3 2 20 9000-9500 200) agg(1 4 1 1 9200-9300 100)"},
	})},
	// A fifth key evicts the least recently used one, sending its sums.
	{name: "a fifth key evicts the oldest", steps: rwaggSteps(rwaggWarmup(4, 0), []rwaggStep{
		{"enter 9 2 3 8000", "hold"}, {"exit 9 2 10 9000", "fold"},
		{"enter 9 1 4 9100", "send"}, {"exit 9 1 1 9200", "send"},
		{"enter 9 1 5 9300", "send"}, {"exit 9 1 1 9400", "send"},
		{"enter 9 1 6 9500", "send"}, {"exit 9 1 1 9600", "send"},
		{"enter 9 1 7 9700", "agg(2 3 1 10 8000-9000 1000) send"},
	})},
	// A loop that runs past the span bound is sent as it runs.
	{name: "the span bound sends a long loop", steps: rwaggSpanSteps()},
	// Threads are apart: one thread's flush never sends another's calls.
	{name: "threads are apart", steps: rwaggSteps(rwaggWarmup(4, 0), []rwaggStep{
		{"enter 9 2 3 8000", "hold"}, {"exit 9 2 10 9000", "fold"},
		{"flush 10", "-"}, {"enter 10 2 3 9100", "send"}, {"forget 10", "-"},
		{"flush 9", "agg(2 3 1 10 8000-9000 1000)"},
	})},
	// A flush inside a held call (signal_generate in a write that raised
	// SIGPIPE) frees the key; the call then cannot fold and is sent with its
	// owed enter.
	{name: "a flush inside a held call", steps: rwaggSteps(rwaggWarmup(4, 0), []rwaggStep{
		{"enter 9 2 3 8000", "hold"}, {"exit 9 2 10 9000", "fold"},
		{"enter 9 2 3 10000", "hold"}, {"flush 9", "agg(2 3 1 10 8000-9000 1000)"},
		{"exit 9 2 10 11000", "enter(2 3 10000) send"},
	})},
	// forget sends the thread's sums and removes its entry.
	{name: "forget sends and removes", steps: rwaggSteps(rwaggWarmup(4, 0), []rwaggStep{
		{"enter 9 2 3 8000", "hold"}, {"exit 9 2 10 9000", "fold"},
		{"forget 9", "agg(2 3 1 10 8000-9000 1000)"}, {"forget 9", "-"},
		{"enter 9 2 3 9100", "send"},
	})},
	// A record that cannot be reserved is counted as lost, and its sums are
	// gone: never sent again by the next flush.
	{name: "a lost flush is counted once", steps: rwaggSteps(rwaggWarmup(4, 0), []rwaggStep{
		{"enter 9 2 3 8000", "hold"}, {"exit 9 2 10 9000", "fold"},
		{"drop 1", "-"}, {"flush 9", "lost"}, {"drop 0", "-"}, {"flush 9", "-"},
	})},
	// A full map means no aggregation for that thread: every call is sent.
	{name: "a full map sends every call", steps: rwaggSteps([]rwaggStep{{"full 1", "-"}},
		rwaggWarmup(6, 0), []rwaggStep{{"flush 9", "-"}})},
}

// rwaggSpanSteps folds hot writes 900 µs apart, each 100 ns long, until
// their span passes 100 ms: the 113th folded call's exit sends the 113.
func rwaggSpanSteps() []rwaggStep {
	steps := rwaggWarmup(4, 0)
	for k := range 113 {
		enter := 8000 + k*900000
		want := "fold"
		if k == 112 {
			want = fmt.Sprintf("agg(2 3 113 1130 8000-%d 11300) fold", enter+100)
		}
		steps = append(steps, rwaggStep{fmt.Sprintf("enter 9 2 3 %d", enter), "hold"},
			rwaggStep{fmt.Sprintf("exit 9 2 10 %d", enter+100), want})
	}
	return append(steps, rwaggStep{"flush 9", "-"})
}

// rwaggMutations are plausible regressions of rwagg.c as {anchor,
// replacement} pairs; each anchor occurs exactly once in the source.
var rwaggMutations = map[string][2]string{
	"no individual calls": {
		"    if (s->streak < IORMESH_RW_INDIVIDUAL)\n        return 1;",
		"    if (0)\n        return 1;",
	},
	"errors fold": {
		"    hot = ret > 0 && now <= enter_ns + IORMESH_RW_HOT_NS;",
		"    hot = now <= enter_ns + IORMESH_RW_HOT_NS;",
	},
	"unfoldable call skips the flush": {
		"    if (!hot) {\n        iormesh_rw_send(st, s, tid);",
		"    if (!hot) {",
	},
	"owed enter not sent": {
		"        s->streak = 0;\n        iormesh_rw_send_held_enter(st, pid, tid, trace_id, enter_ns);",
		"        s->streak = 0;",
	},
	"end time past the folded calls": {
		"        iormesh_rw_send(st, s, tid);\n        s->last_ns = now;",
		"        s->last_ns = now;\n        iormesh_rw_send(st, s, tid);",
	},
	"flush keeps the streak": {
		"        s->trace_id = 0;\n        s->streak = 0;",
		"        s->streak = s->streak;",
	},
	"sums not cleared": {
		"    s->count = 0;\n    s->bytes = 0;",
		"    s->bytes = 0;",
	},
	"no gap check": {
		"    if (now > s->last_ns + IORMESH_RW_HOT_NS) {",
		"    if (0) {",
	},
	"no span bound": {
		"    if (s->count >= IORMESH_RW_MAX_COUNT || now >= s->first_ns + IORMESH_RW_SPAN_NS)",
		"    if (s->count >= IORMESH_RW_MAX_COUNT)",
	},
	"eviction drops the oldest": {
		"        free = iormesh_rw_lru(st);\n        iormesh_rw_send(st, &st->slot[free], tid);",
		"        free = iormesh_rw_lru(st);",
	},
}

// rwaggHarnessSource builds the harness from rwagg.c and the structs and
// defines it uses from maps.h and types.h.
func rwaggHarnessSource(rwaggC, mapsH, typesH string) (string, error) {
	var structs []string
	for _, src := range []struct{ text, name string }{
		{mapsH, "iormesh_rw_slot"}, {mapsH, "iormesh_rw_state"},
		{typesH, "fd_event"}, {typesH, "rw_aggregate_event"},
	} {
		s := regexp.MustCompile(`(?ms)^struct ` + src.name + ` \{.*?^\};`).FindString(src.text)
		if s == "" {
			return "", fmt.Errorf("struct %s not found", src.name)
		}
		structs = append(structs, s)
	}
	slots := regexp.MustCompile(`(?m)^#define IORMESH_RW_SLOTS \d+$`).FindString(mapsH)
	if slots == "" {
		return "", fmt.Errorf("IORMESH_RW_SLOTS not found in maps.h")
	}
	body := strings.Replace(rwaggC, "//+build ignore", "", 1)
	return fmt.Sprintf(rwaggHarnessTemplate, slots, strings.Join(structs, "\n\n"), body), nil
}

func compileRwaggHarness(t *testing.T, rwaggC string) string {
	t.Helper()
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no host C compiler (cc) available")
	}
	src, err := rwaggHarnessSource(rwaggC, rwaggCSource(t, "maps.h"), rwaggCSource(t, "types.h"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "rwagg_harness.c")
	if err := os.WriteFile(srcPath, []byte(src), 0o600); err != nil {
		t.Fatalf("write harness: %v", err)
	}
	binary := filepath.Join(dir, "rwagg_harness")
	out, err := exec.Command(cc, "-O2", "-Wall", "-Werror", "-o", binary, srcPath).CombinedOutput()
	if err != nil {
		t.Fatalf("compile harness: %v\n%s", err, out)
	}
	return binary
}

// rwaggCSource reads one file of internal/c or fails the test.
func rwaggCSource(t *testing.T, name string) string {
	t.Helper()
	src, err := readCSource(name)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// runRwaggScenario runs sc in a fresh harness process and returns one
// message per step whose output differs from the expectation.
func runRwaggScenario(binary string, sc rwaggScenario) []string {
	cmds := make([]string, len(sc.steps))
	for i, step := range sc.steps {
		cmds[i] = step.cmd
	}
	cmd := exec.Command(binary)
	cmd.Stdin = strings.NewReader(strings.Join(cmds, "\n") + "\n")
	out, err := cmd.Output()
	if err != nil {
		return []string{fmt.Sprintf("%s: harness failed: %v\n%s", sc.name, err, out)}
	}
	got := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	var problems []string
	for i, step := range sc.steps {
		line := "<missing>"
		if i < len(got) {
			line = got[i]
		}
		if line != step.want {
			problems = append(problems, fmt.Sprintf("%s: step %d %q printed %q, want %q",
				sc.name, i, step.cmd, line, step.want))
		}
	}
	return problems
}

// TestRwaggHarness runs every scenario against the real rwagg.c.
func TestRwaggHarness(t *testing.T) {
	binary := compileRwaggHarness(t, rwaggCSource(t, "rwagg.c"))
	for _, sc := range rwaggScenarios {
		for _, p := range runRwaggScenario(binary, sc) {
			t.Error(p)
		}
	}
}

// TestRwaggHarnessCatchesMutations: every mutation makes at least one
// scenario fail, so the scenarios pin the behaviour they describe.
func TestRwaggHarnessCatchesMutations(t *testing.T) {
	source := rwaggCSource(t, "rwagg.c")
	for name, m := range rwaggMutations {
		t.Run(name, func(t *testing.T) {
			if strings.Count(source, m[0]) != 1 {
				t.Fatalf("anchor occurs %d times in rwagg.c", strings.Count(source, m[0]))
			}
			binary := compileRwaggHarness(t, strings.Replace(source, m[0], m[1], 1))
			for _, sc := range rwaggScenarios {
				if len(runRwaggScenario(binary, sc)) > 0 {
					return
				}
			}
			t.Fatal("no scenario caught the mutation")
		})
	}
}

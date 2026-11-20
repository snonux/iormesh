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

// The target hint (internal/c/targethint.c, task 9f4) is a bitmap in front
// of target_map: a clear bit makes filter() skip a pid without the hash
// lookup, so a bit lost for a live target is a process nobody traces. The
// kernel's bits are flipped by an atomic add, exact only while a bit is
// added when clear and subtracted when set by one writer at a time. The one
// exception is the group-dead exit on kernels without the tracepoint's
// group_dead field, where two exiting threads can both take the group for
// dead (exec.c, ior_exit_group_dead). As in the restart and rwagg harnesses,
// the real targethint.c helpers and exec.c's iormesh_forget_target are
// compiled with the host C compiler against simulated maps, and each
// scenario also runs against mutated sources.

// targetHintHarnessTemplate simulates the two hint arrays and target_map
// (a small set of tgids). ior_exit_group_dead always reports a dead group,
// as the fallback may for every exiting thread. Commands, one output line
// each:
//
//	add P     the kernel's insert of P (iormesh_hint_set, then target_map)
//	exit P    one group-dead exit of P (iormesh_forget_target)
//	exit2 P   two threads of P exiting at once: every atomic add of both
//	          exits is held back until both have run their checks, which
//	          is the interleaving the fallback allows
//	drop P    userspace deleting P's entry alone (a dismissed exec)
//	hint P    iormesh_target_hint(P): 1 may be a target, 0 surely not
//
// add, exit, exit2 and drop print "ok"; hint prints 1 or 0.
const targetHintHarnessTemplate = `#include <stdio.h>
#include <string.h>

typedef unsigned int __u32;
typedef unsigned long long __u64;
#ifndef __always_inline
#define __always_inline inline __attribute__((always_inline))
#endif

%s

static int target_hint_kern, target_hint_user, target_map;
static __u64 sim_kern[IORMESH_HINT_KERN_WORDS], sim_user[IORMESH_HINT_USER_WORDS];
static __u32 sim_targets[16];
static int sim_defer, sim_pending;
static struct { __u64 *word, delta; } sim_held[8];

static void *bpf_map_lookup_elem(void *map, const void *key) {
    __u32 k = *(const __u32 *)key;

    if (map == &target_hint_kern)
        return k < IORMESH_HINT_KERN_WORDS ? &sim_kern[k] : NULL;
    if (map == &target_hint_user)
        return k < IORMESH_HINT_USER_WORDS ? &sim_user[k] : NULL;
    return NULL;
}

static long bpf_map_delete_elem(void *map, const void *key) {
    for (int i = 0; map == &target_map && i < 16; i++)
        if (sim_targets[i] && sim_targets[i] == *(const __u32 *)key) {
            sim_targets[i] = 0;
            return 0;
        }
    return -2;
}

static void sim_insert(__u32 pid) {
    for (int i = 0; i < 16; i++)
        if (!sim_targets[i]) {
            sim_targets[i] = pid;
            return;
        }
}

static void sim_xadd(__u64 *word, __u64 delta) {
    if (sim_defer && sim_pending < 8) {
        sim_held[sim_pending].word = word;
        sim_held[sim_pending++].delta = delta;
        return;
    }
    *word += delta;
}
#define __sync_fetch_and_add(p, v) sim_xadd((p), (v))

static int ior_exit_group_dead(void *ctx) {
    (void)ctx;
    return 1;
}

%s

int main(void) {
    char cmd[16];
    __u32 pid;

    while (scanf("%%15s %%u", cmd, &pid) == 2) {
        if (!strcmp(cmd, "add")) {
            iormesh_hint_set(pid);
            sim_insert(pid);
        } else if (!strcmp(cmd, "exit")) {
            iormesh_forget_target(NULL, pid);
        } else if (!strcmp(cmd, "exit2")) {
            sim_defer = 1;
            iormesh_forget_target(NULL, pid);
            iormesh_forget_target(NULL, pid);
            sim_defer = 0;
            for (int i = 0; i < sim_pending; i++)
                *sim_held[i].word += sim_held[i].delta;
            sim_pending = 0;
        } else if (!strcmp(cmd, "drop")) {
            bpf_map_delete_elem(&target_map, &pid);
        } else if (!strcmp(cmd, "hint")) {
            printf("%%d\n", iormesh_target_hint(pid) ? 1 : 0);
            continue;
        }
        printf("ok\n");
    }
    return 0;
}
`

type targetHintStep struct{ cmd, want string }

type targetHintScenario struct {
	name  string
	steps []targetHintStep
}

// targetHintScenarios: pids 100 and 101 share a word, as a process and its
// followed child often do.
var targetHintScenarios = []targetHintScenario{
	{name: "double group-dead exit keeps the neighbour", steps: []targetHintStep{
		{"add 100", "ok"}, {"add 101", "ok"}, {"exit2 100", "ok"},
		{"hint 101", "1"}, {"hint 100", "0"},
	}},
	{name: "exit clears the bit and the pid starts clear", steps: []targetHintStep{
		{"add 100", "ok"}, {"exit 100", "ok"}, {"hint 100", "0"},
		{"add 100", "ok"}, {"hint 100", "1"}, {"exit 100", "ok"}, {"hint 100", "0"},
	}},
	{name: "a bit without an entry survives and is reused", steps: []targetHintStep{
		{"add 102", "ok"}, {"add 103", "ok"}, {"drop 102", "ok"}, {"exit 102", "ok"},
		{"hint 102", "1"}, {"add 102", "ok"}, {"exit 102", "ok"},
		{"hint 102", "0"}, {"hint 103", "1"},
	}},
}

// targetHintMutations are regressions of exec.c or targethint.c, each with
// the scenario that must catch it: the clear not gated on the delete (the
// code before the gate; the double exit then borrows pid 101's bit) and the
// set losing its test of the bit.
var targetHintMutations = map[string]struct{ file, from, to, catch string }{
	"clear not gated on the delete": {"exec.c",
		"    if (!bpf_map_delete_elem(&target_map, &tgid))\n        iormesh_hint_clear(tgid);",
		"    bpf_map_delete_elem(&target_map, &tgid);\n    iormesh_hint_clear(tgid);",
		"double group-dead exit keeps the neighbour"},
	"set adds a set bit again": {"targethint.c",
		"if (k && !(*k & bit))\n        __sync_fetch_and_add(k, bit);",
		"if (k)\n        __sync_fetch_and_add(k, bit);",
		"a bit without an entry survives and is reused"},
}

var targetHintDefineRE = regexp.MustCompile(`(?m)^#define IORMESH_HINT_[A-Z_]+ .*$`)

// targetHintHarnessSource cuts the defines and helpers out of targethint.c
// and iormesh_forget_target out of exec.c.
func targetHintHarnessSource(hintC, execC string) (string, error) {
	hintCode, execCode := stripCComments(hintC), stripCComments(execC)
	var functions []string
	for _, name := range []string{"iormesh_hint_kern_word", "iormesh_target_hint",
		"iormesh_hint_set", "iormesh_hint_clear"} {
		fn := extractCFunction(hintCode, name)
		if fn == "" {
			return "", fmt.Errorf("function %s not found in targethint.c", name)
		}
		functions = append(functions, fn)
	}
	forget := extractCFunction(execCode, "iormesh_forget_target")
	if forget == "" {
		return "", fmt.Errorf("function iormesh_forget_target not found in exec.c")
	}
	functions = append(functions, forget)
	defines := strings.Join(targetHintDefineRE.FindAllString(hintC, -1), "\n")
	return fmt.Sprintf(targetHintHarnessTemplate, defines, strings.Join(functions, "\n\n")), nil
}

func compileTargetHintHarness(t *testing.T, hintC, execC string) string {
	t.Helper()
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no host C compiler (cc) available")
	}
	src, err := targetHintHarnessSource(hintC, execC)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "targethint_harness.c")
	if err := os.WriteFile(srcPath, []byte(src), 0o600); err != nil {
		t.Fatalf("write harness: %v", err)
	}
	binary := filepath.Join(dir, "targethint_harness")
	out, err := exec.Command(cc, "-O2", "-Wall", "-Werror", "-o", binary, srcPath).CombinedOutput()
	if err != nil {
		t.Fatalf("compile harness: %v\n%s", err, out)
	}
	return binary
}

// runTargetHintScenario returns one message per step whose output differs.
func runTargetHintScenario(binary string, sc targetHintScenario) []string {
	cmds := make([]string, len(sc.steps))
	for i, step := range sc.steps {
		cmds[i] = step.cmd
	}
	cmd := exec.Command(binary)
	cmd.Stdin = strings.NewReader(strings.Join(cmds, "\n") + "\n")
	out, err := cmd.Output()
	if err != nil {
		return []string{fmt.Sprintf("%s: harness failed: %v", sc.name, err)}
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != len(sc.steps) {
		return []string{fmt.Sprintf("%s: %d lines for %d steps", sc.name, len(lines), len(sc.steps))}
	}
	var problems []string
	for i, step := range sc.steps {
		if lines[i] != step.want {
			problems = append(problems, fmt.Sprintf("%s: step %d %q printed %q, want %q",
				sc.name, i, step.cmd, lines[i], step.want))
		}
	}
	return problems
}

func readTargetHintSources(t *testing.T) (hintC, execC string) {
	t.Helper()
	hintC, err := readCSource("targethint.c")
	if err != nil {
		t.Fatal(err)
	}
	execC, err = readCSource("exec.c")
	if err != nil {
		t.Fatal(err)
	}
	return hintC, execC
}

func TestTargetHintScenarios(t *testing.T) {
	hintC, execC := readTargetHintSources(t)
	binary := compileTargetHintHarness(t, hintC, execC)
	for _, sc := range targetHintScenarios {
		t.Run(sc.name, func(t *testing.T) {
			for _, problem := range runTargetHintScenario(binary, sc) {
				t.Error(problem)
			}
		})
	}
}

// TestTargetHintScenariosCatchRegressions compiles each mutation and
// requires its scenario to fail on it.
func TestTargetHintScenariosCatchRegressions(t *testing.T) {
	hintC, execC := readTargetHintSources(t)
	for name, m := range targetHintMutations {
		t.Run(name, func(t *testing.T) {
			src := map[string]string{"targethint.c": hintC, "exec.c": execC}
			if strings.Count(src[m.file], m.from) != 1 {
				t.Fatalf("mutation anchor must occur exactly once in %s: %q", m.file, m.from)
			}
			src[m.file] = strings.Replace(src[m.file], m.from, m.to, 1)
			binary := compileTargetHintHarness(t, src["targethint.c"], src["exec.c"])
			for _, sc := range targetHintScenarios {
				if sc.name == m.catch && len(runTargetHintScenario(binary, sc)) > 0 {
					return
				}
			}
			t.Fatalf("scenario %q did not catch the regression", m.catch)
		})
	}
}

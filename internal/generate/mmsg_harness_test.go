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

// sendmmsg's and recvmmsg's byte sums (internal/c/mmsg.c, task oh4) come
// from a bounded loop over the caller's mmsghdr array at the exit, and an
// incomplete sum must say so rather than pass for the whole. No root test
// reaches the incomplete cases - the kernel clamps a call to the loop's
// cap, and the array it just wrote cannot fault - so, as for rwagg.c, the
// real mmsg.c is compiled with the host C compiler against a simulated map,
// ring buffer and user memory, with the cap lowered to 4, and driven
// through the cases; each is also run against mutated sources.

// mmsgHarnessTemplate simulates what mmsg.c uses: mmsg_park_map as a small
// table keyed by tid ("full 1" makes inserts fail), the ring buffer
// ("drop 1" makes every reserve fail), bpf_probe_read_user over the
// harness's own array of eight struct mmsghdr ("fault K" makes reads of
// entry K and later fail) and tcpseq.c's iormesh_seq_take, which prints
// the byte count it was given and holds when "seq 1" was set and the count
// is positive. "msgs N l1 .. lN" sets the messages' lengths, "enter T dir"
// and "exit T ret dir" call the two hooks; each command prints one line:
// the take, the record sent ("mmsg(ret bytes sk seq flags)") or "lost",
// then the exit's verdict ("send" or "plain"), or "-".
const mmsgHarnessTemplate = `#include <stdio.h>
#include <string.h>

typedef unsigned int __u32;
typedef int __s32;
typedef unsigned long long __u64;
typedef long long __s64;
#ifndef __always_inline
#define __always_inline inline __attribute__((always_inline))
#endif
#define BPF_ANY 0
#define BPF_MAP_TYPE_LRU_HASH 9
#define __uint(name, val) int (*name)[val]
#define __type(name, val) __typeof__(val) *name
#define SEC(name)
#define IORMESH_MMSG_MAX 4
#define MMSG_EVENT 78
#define MMSG_BYTES_PARTIAL 1

@STRUCT@

struct sim_mmsghdr { unsigned char hdr[56]; __u32 msg_len; __u32 pad; };
static struct sim_mmsghdr sim_msgs[8];
static int sim_fault = 99, sim_seq, sim_drop, sim_full;
static int event_map;
static struct mmsg_event sim_record;
static char sim_out[256];

static void sim_print(const char *s) {
    if (sim_out[0])
        strncat(sim_out, " ", sizeof(sim_out) - strlen(sim_out) - 1);
    strncat(sim_out, s, sizeof(sim_out) - strlen(sim_out) - 1);
}

#define SIM_THREADS 4
static struct { __u32 tid, used; unsigned char value[64]; } sim_map[SIM_THREADS];

static void *bpf_map_lookup_elem(void *map, const void *key) {
    (void)map;
    for (int i = 0; i < SIM_THREADS; i++)
        if (sim_map[i].used && sim_map[i].tid == *(const __u32 *)key)
            return sim_map[i].value;
    return NULL;
}

static long bpf_map_update_elem(void *map, const void *key, const void *value, __u64 flags) {
    (void)flags;
    if (sim_full || bpf_map_lookup_elem(map, key))
        return -1;
    for (int i = 0; i < SIM_THREADS; i++) {
        if (!sim_map[i].used) {
            sim_map[i].used = 1;
            sim_map[i].tid = *(const __u32 *)key;
            memcpy(sim_map[i].value, value, 16);
            return 0;
        }
    }
    return -1;
}

static long bpf_probe_read_user(void *dst, __u32 size, const void *src) {
    long off = (const unsigned char *)src - (const unsigned char *)sim_msgs;
    if (off < 0 || off + size > (long)sizeof(sim_msgs) || off / (long)sizeof(sim_msgs[0]) >= sim_fault)
        return -14;
    memcpy(dst, src, size);
    return 0;
}

static void *bpf_ringbuf_reserve(void *map, __u64 size, __u64 flags) {
    (void)map;
    (void)flags;
    if (sim_drop || size != sizeof(sim_record))
        return NULL;
    memset(&sim_record, 0xA5, sizeof(sim_record));
    return &sim_record;
}

static void bpf_ringbuf_submit(void *data, __u64 flags) {
    struct mmsg_event *ev = data;
    char line[96];
    (void)flags;
    if (ev->event_type != MMSG_EVENT || ev->pid != 77 || ev->trace_id != 5 || ev->time != 1000)
        snprintf(line, sizeof(line), "malformed");
    else
        snprintf(line, sizeof(line), "mmsg(%lld %llu %llx %x %u)", ev->ret, ev->total_bytes, ev->sk,
                 ev->seq, ev->flags);
    sim_print(line);
}

static void ior_count_ringbuf_drop(void) {
    sim_print("lost");
}

static __always_inline int iormesh_seq_take(__u32 tid, __s64 n, __u32 dir, __u64 *sk, __u32 *seq) {
    char line[48];
    (void)tid;
    (void)dir;
    snprintf(line, sizeof(line), "take(%lld)", n);
    sim_print(line);
    if (!sim_seq || n <= 0)
        return 0;
    *sk = 0xabc;
    *seq = 0x10;
    return 1;
}

@BODY@

int main(void) {
    char cmd[16];
    long long a, b, c;
    while (scanf("%15s %lld", cmd, &a) == 2) {
        sim_out[0] = 0;
        if (!strcmp(cmd, "msgs")) {
            for (long long i = 0; i < a && i < 8; i++) {
                if (scanf("%lld", &b) != 1)
                    return 1;
                sim_msgs[i].msg_len = (__u32)b;
            }
        } else if (!strcmp(cmd, "fault")) {
            sim_fault = a;
        } else if (!strcmp(cmd, "seq")) {
            sim_seq = a;
        } else if (!strcmp(cmd, "drop")) {
            sim_drop = a;
        } else if (!strcmp(cmd, "full")) {
            sim_full = a;
        } else if (!strcmp(cmd, "enter") && scanf("%lld", &b) == 1) {
            iormesh_mmsg_on_enter(a, (__u64)sim_msgs, b);
        } else if (!strcmp(cmd, "exit") && scanf("%lld %lld", &b, &c) == 2) {
            sim_print(iormesh_mmsg_on_exit(77, a, 5, 1000, b, c) ? "send" : "plain");
        } else {
            printf("bad command %s\n", cmd);
            return 1;
        }
        printf("%s\n", sim_out[0] ? sim_out : "-");
    }
    return 0;
}
`

// mmsgScenarios reuse rwagg's step and scenario types. Direction 1 is a
// send, 2 a receive (tcpseq.c's IORMESH_SEQ_*).
var mmsgScenarios = []rwaggScenario{
	// The first ret messages' lengths are summed, and the complete sum is
	// what tcp_seq is checked against.
	{name: "a complete sum with tcp_seq", steps: []rwaggStep{
		{"msgs 3 10 20 30", "-"}, {"seq 1", "-"}, {"enter 9 1", "-"},
		{"exit 9 3 1", "take(60) mmsg(3 60 abc 10 0) send"},
	}},
	{name: "a complete sum without tcp_seq", steps: []rwaggStep{
		{"msgs 3 10 20 30", "-"}, {"enter 9 2", "-"},
		{"exit 9 3 2", "take(60) mmsg(3 60 0 0 0) send"},
	}},
	// Only the messages the call moved count.
	{name: "only ret messages count", steps: []rwaggStep{
		{"msgs 3 10 20 30", "-"}, {"enter 9 1", "-"},
		{"exit 9 2 1", "take(30) mmsg(2 30 0 0 0) send"},
	}},
	// Past the cap (4 here) the sum stops and says so; a partial sum is
	// not checked for tcp_seq.
	{name: "the cap makes the sum partial", steps: []rwaggStep{
		{"msgs 6 1 2 3 4 5 6", "-"}, {"seq 1", "-"}, {"enter 9 1", "-"},
		{"exit 9 6 1", "take(0) mmsg(6 10 0 0 1) send"},
	}},
	{name: "a faulting read makes the sum partial", steps: []rwaggStep{
		{"msgs 3 10 20 30", "-"}, {"fault 2", "-"}, {"seq 1", "-"}, {"enter 9 1", "-"},
		{"exit 9 3 1", "take(0) mmsg(3 30 0 0 1) send"},
	}},
	// Without a park there is no array to read: bytes 0, partial.
	{name: "no park", steps: []rwaggStep{
		{"msgs 3 10 20 30", "-"}, {"exit 9 3 1", "take(0) mmsg(3 0 0 0 1) send"},
	}},
	{name: "a refused park", steps: []rwaggStep{
		{"full 1", "-"}, {"enter 9 1", "-"}, {"exit 9 3 1", "take(0) mmsg(3 0 0 0 1) send"},
	}},
	{name: "a park of the other direction", steps: []rwaggStep{
		{"msgs 3 10 20 30", "-"}, {"enter 9 1", "-"}, {"exit 9 3 2", "take(0) mmsg(3 0 0 0 1) send"},
	}},
	// The exit empties the park: a second exit finds none.
	{name: "the exit empties the park", steps: []rwaggStep{
		{"msgs 3 10 20 30", "-"}, {"enter 9 1", "-"},
		{"exit 9 3 1", "take(60) mmsg(3 60 0 0 0) send"},
		{"exit 9 3 1", "take(0) mmsg(3 0 0 0 1) send"},
	}},
	// A call that moved nothing sends its plain ret_event, after emptying
	// both parks.
	{name: "nothing moved", steps: []rwaggStep{
		{"msgs 3 10 20 30", "-"}, {"enter 9 1", "-"}, {"exit 9 0 1", "take(0) plain"},
		{"enter 9 1", "-"}, {"exit 9 -11 1", "take(0) plain"},
		{"exit 9 3 1", "take(0) mmsg(3 0 0 0 1) send"},
	}},
	// Threads are apart.
	{name: "threads are apart", steps: []rwaggStep{
		{"msgs 3 10 20 30", "-"}, {"enter 9 1", "-"}, {"exit 8 3 1", "take(0) mmsg(3 0 0 0 1) send"},
		{"exit 9 3 1", "take(60) mmsg(3 60 0 0 0) send"},
	}},
	// A full ring loses the record, counted; the handler sends no
	// ret_event either.
	{name: "a full ring", steps: []rwaggStep{
		{"msgs 3 10 20 30", "-"}, {"drop 1", "-"}, {"enter 9 1", "-"},
		{"exit 9 3 1", "take(60) lost send"},
	}},
}

// mmsgMutations are plausible regressions of mmsg.c as {anchor,
// replacement} pairs; each anchor occurs exactly once in the source.
var mmsgMutations = map[string][2]string{
	"the sum ignores ret": {"        if (i >= n)\n            break;", "        if (i >= 8)\n            break;"},
	"a fault is not partial": {
		"            flags = MMSG_BYTES_PARTIAL;\n            break;", "            break;",
	},
	"the cap is not partial": {
		"        n = IORMESH_MMSG_MAX;\n        flags = MMSG_BYTES_PARTIAL;", "        n = IORMESH_MMSG_MAX;",
	},
	"the park is not emptied":       {"    if (slot)\n        slot->dir = 0;", ""},
	"a partial sum is checked":      {"flags ? 0 : (__s64)bytes", "(__s64)bytes"},
	"the direction is ignored":      {"slot && slot->dir == dir", "slot && slot->dir"},
	"nothing moved sends a record":  {"    if (ret <= 0)\n        return 0;\n    iormesh_mmsg_emit", "    iormesh_mmsg_emit"},
	"the seq park is left in place": {"    iormesh_seq_take(tid, flags ? 0 : (__s64)bytes, dir, &sk, &seq);\n", ""},
}

// mmsgHarnessSource builds the harness from mmsg.c and struct mmsg_event
// of types.h.
func mmsgHarnessSource(mmsgC, typesH string) (string, error) {
	s := regexp.MustCompile(`(?ms)^struct mmsg_event \{.*?^\};`).FindString(typesH)
	if s == "" {
		return "", fmt.Errorf("struct mmsg_event not found in types.h")
	}
	body := strings.Replace(mmsgC, "//+build ignore", "", 1)
	src := strings.Replace(mmsgHarnessTemplate, "@STRUCT@", s, 1)
	return strings.Replace(src, "@BODY@", body, 1), nil
}

func compileMmsgHarness(t *testing.T, mmsgC string) string {
	t.Helper()
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no host C compiler (cc) available")
	}
	src, err := mmsgHarnessSource(mmsgC, rwaggCSource(t, "types.h"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "mmsg_harness.c")
	if err := os.WriteFile(srcPath, []byte(src), 0o600); err != nil {
		t.Fatalf("write harness: %v", err)
	}
	binary := filepath.Join(dir, "mmsg_harness")
	out, err := exec.Command(cc, "-O2", "-Wall", "-Werror", "-Wno-unknown-pragmas", "-o", binary, srcPath).CombinedOutput()
	if err != nil {
		t.Fatalf("compile harness: %v\n%s", err, out)
	}
	return binary
}

// TestMmsgHarness runs every scenario against the real mmsg.c.
func TestMmsgHarness(t *testing.T) {
	binary := compileMmsgHarness(t, rwaggCSource(t, "mmsg.c"))
	for _, sc := range mmsgScenarios {
		for _, p := range runRwaggScenario(binary, sc) {
			t.Error(p)
		}
	}
}

// TestMmsgHarnessCatchesMutations: every mutation makes at least one
// scenario fail, so the scenarios pin the behaviour they describe.
func TestMmsgHarnessCatchesMutations(t *testing.T) {
	source := rwaggCSource(t, "mmsg.c")
	for name, m := range mmsgMutations {
		t.Run(name, func(t *testing.T) {
			if strings.Count(source, m[0]) != 1 {
				t.Fatalf("anchor occurs %d times in mmsg.c", strings.Count(source, m[0]))
			}
			binary := compileMmsgHarness(t, strings.Replace(source, m[0], m[1], 1))
			for _, sc := range mmsgScenarios {
				if len(runRwaggScenario(binary, sc)) > 0 {
					return
				}
			}
			t.Fatal("no scenario caught the mutation")
		})
	}
}

package correlate

import (
	"fmt"
	"slices"
	"strings"

	"iormesh/internal/agentspool"
	"iormesh/internal/schema"
)

// NativeMark is one kernel Java mark (a *types.JavaMarkEvent,
// internal/c/javamark.c, task td4): thread TID of process PID entered a
// syscall at EnterBoot (the enter record's own boot-clock stamp) from
// inside the JDK native whose uprobe carries Cookie (0: nested too deep,
// the innermost native is unknown), entered at EntryMono on
// CLOCK_MONOTONIC.
type NativeMark struct {
	PID, TID  uint32
	EnterBoot uint64
	Cookie    uint64
	EntryMono uint64
}

// MarkedNative is one native whose syscalls the mark names: its JNI
// symbol and the cookie its uprobe is to carry (JavaJoinConfig.AttachMarks).
type MarkedNative struct {
	Symbol string
	Cookie uint64
}

// markQueue holds one thread's marks in enter order, oldest first from
// head. A mark pairs with its syscall's event by thread and time: the
// kernel sends it right before the enter record, stamped with the
// enter's own clock read, which the event keeps as its boot_ns.
//
// A queue per thread, not one map keyed by (tid, time): a thread issues
// one syscall at a time, so its marks arrive in enter order and its
// events leave the correlator in that same order — the join then only
// ever looks at the head. That order is an assumption the join depends
// on (the correlator emits in completion order, which for one thread is
// enter order; a fold precedes the thread's next call): if it broke, a
// mark would be skipped as stale before its event came and the event
// would stay none, never be misattributed — JavaStats.MarkSkipped counts
// those skips. That spares a map insert, lookup and delete
// with a 16-byte key per event, which with the S3 burst's every event
// marked was a tenth of the join's CPU (task ud4).
type markQueue struct {
	marks []NativeMark
	head  int
}

// Mark files a kernel Java mark until its syscall's event arrives. The
// mark always comes first: the kernel sends it ahead of the enter, and
// the event leaves the correlator only after the exit.
func (j *JavaJoin) Mark(m NativeMark) {
	q := j.marks[m.TID]
	if q == nil {
		q = &markQueue{}
		j.marks[m.TID] = q
	}
	q.marks = append(q.marks, m)
	// Read before written: nearly every mark is of a thread already
	// marked, and a lookup costs less than a store.
	if v := j.jvms[m.PID]; v != nil && v.markedTIDs != nil && !v.markedTIDs[int32(m.TID)] {
		v.markedTIDs[int32(m.TID)] = true
	}
}

// takeMark returns and forgets the mark of ev's syscall. Marks of the
// thread entered before ev's syscall are forgotten on the way: their
// events have passed (an event that is not joined never asks for its
// mark) or will never come (a lost exit), since the thread's events
// arrive in enter order.
func (j *JavaJoin) takeMark(ev *schema.Event) (NativeMark, bool) {
	q := j.marks[ev.TID]
	if q == nil {
		return NativeMark{}, false
	}
	for q.head < len(q.marks) && q.marks[q.head].EnterBoot < ev.BootNS {
		q.head++
		j.stats.MarkSkipped++
	}
	// A head newer than ev (or none) is the normal unmarked case — a
	// syscall from outside every hooked native, counted as Unmarked by
	// attribute — not a miss.
	var m NativeMark
	ok := q.head < len(q.marks) && q.marks[q.head].EnterBoot == ev.BootNS
	if ok {
		m = q.marks[q.head]
		q.head++
	}
	q.compact()
	return m, ok && m.PID == ev.PID
}

// pendingMarks counts the marks still waiting for their event.
func (j *JavaJoin) pendingMarks() int {
	n := 0
	for _, q := range j.marks {
		n += len(q.marks) - q.head
	}
	return n
}

// compact reclaims the taken marks' room, as JavaJoin.compact does for
// held events: a busy thread's queue is rarely empty — its events wait
// for the watermark while its next marks arrive — so an emptied queue is
// rewound, and otherwise the live part moves down once the taken part is
// the larger, keeping appends from growing the array without end.
func (q *markQueue) compact() {
	switch {
	case q.head == len(q.marks):
		q.marks, q.head = q.marks[:0], 0
	case q.head > len(q.marks)/2:
		n := copy(q.marks, q.marks[q.head:])
		q.marks, q.head = q.marks[:n], 0
	}
}

// pruneMarks forgets marks whose event cannot come any more: the
// correlator holds an event at most its fuse window, well inside Retain,
// and a JVM's event waits at most Timeout here. A mark whose enter was
// lost, or whose event never reached a JVM's join, would otherwise stay;
// so would the queue of a thread that exited.
func (j *JavaJoin) pruneMarks() {
	keep := j.cfg.Retain + j.cfg.Timeout
	if j.scanned <= keep {
		return
	}
	cut := j.scanned - keep
	for tid, q := range j.marks {
		for q.head < len(q.marks) && q.marks[q.head].EnterBoot < cut {
			q.head++
		}
		q.compact()
		if len(q.marks) == 0 {
			delete(j.marks, tid)
		}
	}
}

// cookie returns the id of the native named symbol, numbering a native
// the first time a hook table names it.
func (j *JavaJoin) cookie(symbol string) uint64 {
	if c, ok := j.cookies[symbol]; ok {
		return c
	}
	c := uint64(len(j.cookies) + 1)
	j.cookies[symbol] = c
	return c
}

// attachMarks probes v's natives once its whole hook table has been read
// (jvm.tableRead: a record that follows the table). Not at the first hook:
// the agent writes the table one record at a time while the tracer drains
// concurrently, and a drain that caught only its first rows probed only
// their natives — the I/O of every later hook (unlink0, say) stayed none
// for the JVM's life, though the tracer had reported it joined (task tf4).
// A JVM whose natives cannot be probed keeps every syscall unmarked —
// none, never guessed — and the reason goes to Warn.
func (j *JavaJoin) attachMarks(v *jvm) {
	if v.probed || len(v.hooks) == 0 || !v.tableRead {
		return
	}
	v.probed = true
	if j.cfg.AttachMarks == nil {
		return
	}
	natives := v.natives()
	missing, err := j.cfg.AttachMarks(v.pid, natives)
	switch {
	case err != nil:
		j.stats.MarkFailed++
		j.warn(fmt.Errorf("java mark, pid %d: %w", v.pid, err))
	case len(missing) == len(natives):
		// A JDK without Java_* symbols (stripped, static): nothing to
		// probe, so the JVM is as unmarked as one whose attach failed.
		j.stats.MarkFailed++
		j.warn(fmt.Errorf("java mark, pid %d: none of the %d natives found, its I/O stays unattributed",
			v.pid, len(natives)))
	default:
		v.probedMono = j.mono(j.cfg.Clock())
		v.markedTIDs, v.afterProbe = map[int32]bool{}, map[int32]int{}
	}
	if err == nil && len(missing) > 0 && len(missing) < len(natives) {
		j.warn(fmt.Errorf("java mark, pid %d: natives not found, their I/O stays unattributed: %s",
			v.pid, strings.Join(missing, ", ")))
	}
}

// natives lists the distinct natives v's hooks reach, by symbol.
func (v *jvm) natives() []MarkedNative {
	var out []MarkedNative
	for _, h := range v.hooks {
		if !slices.ContainsFunc(out, func(n MarkedNative) bool { return n.Symbol == h.symbol }) {
			out = append(out, MarkedNative{Symbol: h.symbol, Cookie: h.cookie})
		}
	}
	slices.SortFunc(out, func(a, b MarkedNative) int { return strings.Compare(a.Symbol, b.Symbol) })
	return out
}

func (j *JavaJoin) warn(err error) {
	if j.cfg.Warn != nil {
		j.cfg.Warn(err)
	}
}

// noMarkBrackets is how many brackets begun after a JVM's probe, on
// threads that never sent a mark, make markCheck call the mark dead. Not
// one: a bracket may hold no syscall (a read the JVM answered from its
// buffer), and a thread's folded reads and writes send no mark (the fold
// precedes the mark hook) — but dozens of brackets with not one mark on
// their threads are not that.
const noMarkBrackets = 64

// countAfterProbe counts a bracket that began after v's natives were
// probed, per thread, for markCheck — only on threads not marked yet:
// markCheck ignores the others, and a thread once marked stays so, so
// skipping them changes no verdict and spares a map store per bracket.
func (v *jvm) countAfterProbe(b agentspool.Bracket) {
	if v.afterProbe != nil && b.EnterMono > v.probedMono && !v.markedTIDs[b.KTID] {
		v.afterProbe[b.KTID]++
	}
}

// markCheck warns, once per JVM, and counts it as unmarked, when its
// threads keep closing brackets after the probe without one mark from
// them: the uprobes are attached but do not fire on those threads. Linux
// 6.6 to 6.9 filtered uprobe_multi links by thread instead of process
// (fixed by 46ba0e49, "bpf: fix multi-uprobe PID filtering logic", in
// 6.10 and the stable kernels it was backported to): only the thread whose
// tid is the pid (a JVM's launcher thread) would be marked, and every other
// thread's I/O would stay none without a word.
func (j *JavaJoin) markCheck(v *jvm) {
	if v.afterProbe == nil || v.markDead {
		return
	}
	n := 0
	for tid, c := range v.afterProbe {
		if !v.markedTIDs[tid] {
			n += c
		}
	}
	if n < noMarkBrackets {
		return
	}
	v.markDead = true
	j.stats.MarkFailed++
	j.warn(fmt.Errorf("java mark, pid %d: %d brackets since the probe on threads that sent no mark — "+
		"does this kernel filter uprobe_multi by thread (Linux 6.6–6.9 without commit 46ba0e49)? "+
		"Their I/O stays unattributed", v.pid, n))
}

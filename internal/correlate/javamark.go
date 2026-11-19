package correlate

import (
	"fmt"
	"slices"
	"strings"

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

// markKey pairs a mark with its syscall's event: the kernel sends the
// mark right before the enter record, stamped with the enter's own clock
// read, which the event keeps as its boot_ns.
type markKey struct {
	tid  uint32
	boot uint64
}

// Mark files a kernel Java mark until its syscall's event arrives. The
// mark always comes first: the kernel sends it ahead of the enter, and
// the event leaves the correlator only after the exit.
func (j *JavaJoin) Mark(m NativeMark) {
	j.marks[markKey{tid: m.TID, boot: m.EnterBoot}] = m
}

// takeMark returns and forgets the mark of ev's syscall.
func (j *JavaJoin) takeMark(ev *schema.Event) (NativeMark, bool) {
	k := markKey{tid: ev.TID, boot: ev.BootNS}
	m, ok := j.marks[k]
	if ok {
		delete(j.marks, k)
	}
	return m, ok && m.PID == ev.PID
}

// pruneMarks forgets marks whose event cannot come any more: the
// correlator holds an event at most its fuse window, well inside Retain,
// and a JVM's event waits at most Timeout here. A mark whose enter was
// lost, or whose event never reached a JVM's join, would otherwise stay.
func (j *JavaJoin) pruneMarks() {
	keep := j.cfg.Retain + j.cfg.Timeout
	if j.scanned <= keep {
		return
	}
	cut := j.scanned - keep
	for k := range j.marks {
		if k.boot < cut {
			delete(j.marks, k)
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

// attachMarks probes v's natives once its hook table has been read: the
// agent writes the table into the empty ring at install, so the first
// drain that saw a hook saw them all. A JVM whose natives cannot be
// probed keeps every syscall unmarked — none, never guessed — and the
// reason goes to Warn.
func (j *JavaJoin) attachMarks(v *jvm) {
	if v.probed || len(v.hooks) == 0 {
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
	case len(missing) > 0:
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

package correlate

import (
	"errors"
	"slices"
	"sort"

	"iormesh/internal/agentspool"
	"iormesh/internal/schema"
)

// startTolerance is how much earlier than a process's start (clock-tick
// resolution from /proc, 10 ms) its ring may seem to have been created
// and still be its own: a ring created before the process started is a
// stale one of an earlier process with the same pid.
const startTolerance = 20_000_000

// maxScan bounds the brackets innermost looks at past the first one that
// ends after the syscall: they are the thread's later siblings inside
// the containing bracket, few in practice. A container farther away is
// not found, and the event stays none — never misattributed.
const maxScan = 256

// overlapScan bounds overlapped's look past the innermost container: the
// agent's bracket depth limit.
const overlapScan = 16

// jvm is the joiner's state for one agent ring: everything read from it
// so far.
type jvm struct {
	ring    AgentRing
	pid     uint32
	start   uint64 // the process's start (/proc), 0 if it was gone
	created uint64 // the ring's creation, boot clock
	// createdMono is the ring's creation on its own clock: no bracket
	// begins before it.
	createdMono uint64
	failed      bool // the ring is corrupt: decide with what was read
	// hooks maps a hook id to the event kinds its native issues.
	hooks map[int32][]string
	// threads by Java thread id; names maps a platform thread's kernel
	// tid to its name, for the carrier of a virtual thread's I/O.
	threads map[uint64]agentspool.Thread
	names   map[int32]string
	frames  map[uint64][]string // agent stack id → frames
	stacks  map[uint64][]uint64 // agent stack id → method ids, resolved
	// brackets per kernel tid, by exit time: a thread closes its
	// brackets one after another, so ring order is exit order per tid.
	brackets  map[int32][]agentspool.Bracket
	watermark uint64 // the latest WATERMARK, CLOCK_MONOTONIC
}

func newJVM(r AgentRing, pid uint32, start, createdMono, created uint64) *jvm {
	return &jvm{ring: r, pid: pid, start: start, created: created, createdMono: createdMono,
		hooks: map[int32][]string{}, threads: map[uint64]agentspool.Thread{},
		names: map[int32]string{}, frames: map[uint64][]string{},
		stacks: map[uint64][]uint64{}, brackets: map[int32][]agentspool.Bracket{}}
}

// owns says whether ev is of this ring's process, not of an earlier or
// later one with the same pid. The start times come from the same /proc
// field; when the JVM was gone before its ring was opened (start 0) the
// ring's creation time decides.
func (v *jvm) owns(ev *schema.Event) bool {
	if v.start != 0 {
		return v.start == ev.PIDInc
	}
	return v.created+startTolerance >= ev.PIDInc
}

// drain reads every record the ring has published since the last drain,
// writing THREAD records to the thread table, and forgets brackets the
// watermark has left Retain behind. A corrupt ring is read up to the
// damage and then only counted: its JVM's events are decided with what
// was read, so a broken agent cannot stall the trace.
func (j *JavaJoin) drain(v *jvm) error {
	if v.failed {
		return nil
	}
	var werr error
	_, err := v.ring.Drain(func(rec agentspool.Record) {
		if werr == nil {
			werr = j.record(v, rec)
		}
	})
	if errors.Is(err, agentspool.ErrCorrupt) {
		v.failed = true
		j.stats.Corrupt++
		err = nil
	}
	v.prune(j.cfg.Retain)
	return errors.Join(err, werr)
}

// record files one ring record.
func (j *JavaJoin) record(v *jvm, rec agentspool.Record) error {
	switch r := rec.(type) {
	case agentspool.Hook:
		v.hooks[r.ID] = eventKinds(r.Syscalls)
	case agentspool.Thread:
		return j.thread(v, r)
	case agentspool.Stack:
		v.frames[r.ID] = r.Frames
	case agentspool.Watermark:
		v.watermark = max(v.watermark, r.Mono)
	case agentspool.Bracket:
		j.stats.Brackets++
		if r.KTID > 0 {
			v.addBracket(r)
		}
	}
	return nil
}

// thread files a THREAD record and writes it to the thread table. Only a
// platform thread's tid names a carrier: a virtual thread's is just
// where it happened to start.
func (j *JavaJoin) thread(v *jvm, r agentspool.Thread) error {
	v.threads[r.ID] = r
	if !r.Virtual && r.KTID > 0 {
		v.names[r.KTID] = r.Name
	}
	return j.cfg.Tables.Thread(schema.Thread{SchemaVersion: schema.Version, PID: v.pid,
		PIDInc: v.start, TID: uint32(r.KTID), JavaThreadID: r.ID, Name: r.Name,
		Virtual: r.Virtual})
}

// addBracket keeps the tid's list sorted by exit. Ring order is exit order
// per tid, so this appends; the backward search covers the carriers of
// virtual threads, where two of them may publish out of order.
func (v *jvm) addBracket(b agentspool.Bracket) {
	list := v.brackets[b.KTID]
	i := len(list)
	for i > 0 && list[i-1].ExitMono > b.ExitMono {
		i--
	}
	v.brackets[b.KTID] = slices.Insert(list, i, b)
}

// prune forgets the brackets that exited more than retain before the
// watermark; no event still to come can sit in them.
func (v *jvm) prune(retain uint64) {
	if v.watermark <= retain {
		return
	}
	cut := v.watermark - retain
	for tid, list := range v.brackets {
		i := sort.Search(len(list), func(i int) bool { return list[i].ExitMono >= cut })
		switch {
		case i == len(list):
			delete(v.brackets, tid)
		case i > 0:
			v.brackets[tid] = slices.Delete(list, 0, i)
		}
	}
}

// innermost finds the innermost bracket on tid whose window contains t.
// Per thread, brackets nest or are disjoint, so the innermost container is
// the first bracket in exit order that ends at or after t and began at or
// before it (those before it that began after t are its later children).
// ambiguous: see overlapped.
func (v *jvm) innermost(tid int32, t uint64) (b agentspool.Bracket, found, ambiguous bool) {
	list := v.brackets[tid]
	i := sort.Search(len(list), func(i int) bool { return list[i].ExitMono >= t })
	for k := i; k < min(len(list), i+maxScan); k++ {
		if list[k].EnterMono <= t {
			return list[k], true, overlapped(list[k+1:], list[k], t)
		}
	}
	return b, false, false
}

// overlapped reports whether a further container of t, among the next
// brackets in exit order, began after b: the two overlap without nesting,
// which one thread's bracket stack cannot produce (the carrier of two
// virtual threads can), so neither may be chosen. Only the next
// overlapScan brackets are looked at — the platform-thread case needs
// none, and the cap keeps a busy thread's events from scanning its whole
// future.
func overlapped(next []agentspool.Bracket, b agentspool.Bracket, t uint64) bool {
	for _, c := range next[:min(len(next), overlapScan)] {
		if c.EnterMono <= t {
			return c.EnterMono > b.EnterMono
		}
	}
	return false
}

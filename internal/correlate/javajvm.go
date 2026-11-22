package correlate

import (
	"errors"
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

// hookInfo is a hook's syscall allowlist as event kinds, whether its
// bracket can span a virtual thread's park (canPark), and the native it
// reaches: its JNI symbol and the cookie that native's marks carry.
type hookInfo struct {
	kinds  []string
	parks  bool
	symbol string
	cookie uint64
}

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
	// tableRead: a record other than HOOK was read. The agent writes its
	// whole hook table into the empty ring first and only then anything
	// else (Agent.prepare: warmUp's THREAD and WATERMARK follow the
	// table, every bracket follows the retransformation), and the ring
	// hands records out in reservation order, so from then on v.hooks is
	// complete. A drain can land in the middle of the table (task tf4).
	// A resent table (resending) is complete only at a WATERMARK.
	tableRead bool
	// resending: a HOOK row came after other records before the probe —
	// this session opened a ring an earlier one drained, and the agent
	// resends its table for it (task 5j4, Watermarks.resync) while the
	// JVM's threads keep writing, so their records say nothing about the
	// table. The agent closes a resent table with a WATERMARK from the
	// thread that wrote the rows, and writes no watermark while rows it
	// resent were dropped (it retries the whole table first), so the
	// first WATERMARK after them follows a whole table; only that sets
	// tableRead again.
	resending bool
	// probed: the mark's uprobes were attached to this JVM's natives (or
	// the attempt failed); done once, after the whole hook table was read.
	probed bool
	// The mark's liveness (markCheck, javamark.go): when the probe was
	// attached, the threads that sent a mark since, and per thread the
	// brackets that began after the probe.
	probedMono uint64
	markedTIDs map[int32]bool
	afterProbe map[int32]int
	markDead   bool // the "no marks" warning was given
	// hooks maps a hook id to what the joiner needs of its HOOK row.
	hooks map[int32]hookInfo
	// threads by Java thread id; names maps a platform thread's kernel
	// tid to its name, for the carrier of a virtual thread's I/O.
	threads map[uint64]agentspool.Thread
	names   map[int32]string
	// renames lists each renamed thread's later names (task fe4),
	// in the order the agent wrote them (javaclass.go).
	renames map[uint64][]rename
	frames  map[uint64]agentspool.Stack // agent stack id → frames, class ids
	stacks  map[uint64][]uint64         // agent stack id → method ids, resolved
	// classes are the CLASS records by agent class id, classIDs the
	// trace's class ids they were given (written when a frame names one).
	classes  map[uint64]agentspool.Class
	classIDs map[uint64]uint64
	// brackets per kernel tid, by exit time: a thread closes its
	// brackets one after another, so ring order is exit order per tid.
	// Held by pointer, so adding a bracket is a map lookup, not a store.
	brackets  map[int32]*bracketList
	watermark uint64 // the latest WATERMARK, CLOCK_MONOTONIC
	// asyncFrom is the earliest sendAsync marker of the JVM (CLOCK_
	// MONOTONIC, 0: none; task wd4 review round 2): java.net.http client
	// requests completing from then on get no req_seq.
	asyncFrom uint64
	// comm and inc are the JVM's as its last joined event carried them,
	// for its http events' envelope.
	comm string
	inc  uint64
}

// incarnation is the JVM's pid_incarnation: its start from /proc, or,
// when it was gone before its ring was opened, its events'.
func (v *jvm) incarnation() uint64 {
	if v.start != 0 {
		return v.start
	}
	return v.inc
}

func newJVM(r AgentRing, pid uint32, start, createdMono, created uint64) *jvm {
	return &jvm{ring: r, pid: pid, start: start, created: created, createdMono: createdMono,
		hooks: map[int32]hookInfo{}, threads: map[uint64]agentspool.Thread{},
		names: map[int32]string{}, renames: map[uint64][]rename{},
		frames: map[uint64]agentspool.Stack{}, stacks: map[uint64][]uint64{},
		classes: map[uint64]agentspool.Class{}, classIDs: map[uint64]uint64{},
		brackets: map[int32]*bracketList{}}
}

// owns says whether ev is of this ring's process, not of an earlier or
// later one with the same pid. The start times come from the same /proc
// field. A ring whose start was unknown when it was opened (start 0) is
// never trusted (isTraced, task yi4), so an accepted ring always takes
// the first branch; the creation-time fallback below is kept only as
// the conservative answer for a jvm built outside that path.
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
	d := drainer{j: j, v: v}
	_, err := v.ring.DrainTo(&d)
	if errors.Is(err, agentspool.ErrCorrupt) {
		v.failed = true
		j.stats.Corrupt++
		err = nil
	}
	v.prune(j.cfg.Retain)
	return errors.Join(err, d.err)
}

// drainer files one drain's records into v (agentspool.Visitor): the
// bracket and the watermark every hooked call sends come unboxed (task
// ee4), the rest through record. The first table error stops the filing
// of every later record but not the drain.
type drainer struct {
	j   *JavaJoin
	v   *jvm
	err error
}

func (d *drainer) Bracket(b *agentspool.Bracket) {
	d.v.otherRecord()
	if d.err == nil {
		d.j.bracket(d.v, b)
	}
}

func (d *drainer) Watermark(w agentspool.Watermark) {
	// Closes the table, a resent one too (jvm.resending).
	d.v.tableRead, d.v.resending = true, false
	if d.err == nil {
		d.v.watermark = max(d.v.watermark, w.Mono)
	}
}

func (d *drainer) Record(rec agentspool.Record) {
	switch rec.(type) {
	case agentspool.Hook:
		d.v.hookRow()
	case agentspool.Watermark: // a boxed one, as Watermark above
		d.v.tableRead, d.v.resending = true, false
	default:
		d.v.otherRecord()
	}
	if d.err == nil {
		d.err = d.j.record(d.v, rec)
	}
}

// hookRow notes a HOOK record: one after other records, before the
// natives were probed, starts a resent table (jvm.resending). After the
// probe a resent row (another session asked for it) changes nothing.
func (v *jvm) hookRow() {
	if v.tableRead && !v.probed {
		v.tableRead, v.resending = false, true
	}
}

// otherRecord notes a record other than HOOK and WATERMARK: it follows
// the install's table, but not necessarily a resent one.
func (v *jvm) otherRecord() {
	if !v.resending {
		v.tableRead = true
	}
}

// bracket files a BRACKET record: one of a known thread (KTID > 0) joins
// that thread's list.
func (j *JavaJoin) bracket(v *jvm, b *agentspool.Bracket) {
	j.stats.Brackets++
	if b.KTID > 0 {
		v.addBracket(b)
		v.countAfterProbe(*b)
	}
}

// record files one ring record.
func (j *JavaJoin) record(v *jvm, rec agentspool.Record) error {
	switch r := rec.(type) {
	case agentspool.Hook:
		sym := r.Symbol()
		v.hooks[r.ID] = hookInfo{kinds: eventKinds(r.Syscalls), parks: canPark(r),
			symbol: sym, cookie: j.cookie(sym)}
	case agentspool.Thread:
		return j.thread(v, r)
	case agentspool.Stack:
		v.frames[r.ID] = r
	case agentspool.Class:
		v.classes[r.ID] = r
	case agentspool.Watermark:
		v.watermark = max(v.watermark, r.Mono)
	case agentspool.Bracket:
		j.bracket(v, &r)
	case agentspool.HTTP:
		if r.AsyncMark {
			if v.asyncFrom == 0 || r.ExitMono < v.asyncFrom {
				v.asyncFrom = r.ExitMono
			}
			return nil
		}
		j.addHTTP(v, r)
	case agentspool.DNS:
		j.addDNS(v, r)
	}
	return nil
}

// thread files a THREAD record and writes it to the thread table: a
// thread's first, or a rename (a later one, task fe4), which names the
// thread's later brackets — one row per name change, none for a record
// repeating the name in force. Only a platform thread's tid names a
// carrier: a virtual thread's is just where it happened to start. A
// carrier is named after its latest name.
func (j *JavaJoin) thread(v *jvm, r agentspool.Thread) error {
	if first, ok := v.threads[r.ID]; ok {
		if !v.rename(first, r.Name) {
			return nil
		}
	} else {
		v.threads[r.ID] = r
	}
	if !r.Virtual && r.KTID > 0 {
		v.names[r.KTID] = r.Name
	}
	return j.cfg.Tables.Thread(schema.Thread{SchemaVersion: schema.Version, PID: v.pid,
		PIDInc: v.start, TID: uint32(r.KTID), JavaThreadID: r.ID, Name: r.Name,
		Virtual: r.Virtual})
}

// addBracket files b in its thread's list (bracketList.add).
func (v *jvm) addBracket(b *agentspool.Bracket) {
	bl := v.brackets[b.KTID]
	if bl == nil {
		bl = newBracketList(bracketChunkShift)
		v.brackets[b.KTID] = bl
	}
	bl.add(b)
}

// prune forgets the brackets that exited more than retain before the
// watermark; no event still to come can sit in them. It drops them off
// the list's front instead of moving the kept ones down: a busy thread
// keeps Retain's worth of brackets (10 s, up to millions), and a drain
// every 50 ms copying all of them would cost more than the join itself
// (task ud4).
func (v *jvm) prune(retain uint64) {
	if v.watermark <= retain {
		return
	}
	cut := v.watermark - retain
	for tid, bl := range v.brackets {
		i := exitSearch(bl, cut, 0)
		switch {
		case i == bl.len():
			delete(v.brackets, tid)
		case i > 0:
			bl.dropFront(i)
		}
	}
}

// innermost finds the innermost bracket on tid whose window contains t.
// Per thread, brackets nest or are disjoint, so the innermost container is
// the first bracket in exit order that ends at or after t and began at or
// before it (those before it that began after t are its later children).
// ambiguous: see overlapped.
func (v *jvm) innermost(tid int32, t uint64) (b agentspool.Bracket, found, ambiguous bool) {
	bl := v.brackets[tid]
	if bl == nil {
		return b, false, false
	}
	i := exitSearch(bl, t, bl.hint)
	bl.hint = i
	for k := i; k < min(bl.len(), i+maxScan); k++ {
		if c := bl.at(k); c.EnterMono <= t {
			return *c, true, overlapped(bl, k+1, *c, t)
		}
	}
	return b, false, false
}

// exitSearch is sort.Search for the first bracket of list (in exit
// order) that exits at or after t, galloping from hint (the last answer)
// first. A busy thread's list holds Retain's worth of brackets (10 s, a
// million on the S3 burst), while consecutive searches of a thread land
// next to each other: doubling steps from the hint bound the binary
// search to a short, cached stretch instead of halving down from the
// whole list (task ud4). Any hint gives the right answer; a good one
// only makes it cheap.
func exitSearch(l *bracketList, t uint64, hint int) int {
	lo, hi := 0, l.len() // the answer lies in [lo, hi]
	hint = min(max(hint, 0), l.len())
	if hint > 0 && l.at(hint-1).ExitMono >= t {
		hi = hint - 1 // the answer is before the hint: gallop down
		for step := 1; hi > lo; step *= 2 {
			k := max(hi-step, lo)
			if l.at(k).ExitMono < t {
				lo = k + 1
				break
			}
			hi = k
		}
	} else {
		lo = hint // every bracket before the hint exits before t: gallop up
		for step := 1; lo < hi; step *= 2 {
			k := min(lo+step-1, hi-1)
			if l.at(k).ExitMono >= t {
				hi = k
				break
			}
			lo = k + 1
		}
	}
	return lo + sort.Search(hi-lo, func(i int) bool { return l.at(lo+i).ExitMono >= t })
}

// overlapped reports whether a further container of t, among the next
// brackets in exit order, began after b: the two overlap without nesting,
// which one thread's bracket stack cannot produce (the carrier of two
// virtual threads can), so neither may be chosen. Only the next
// overlapScan brackets are looked at — the platform-thread case needs
// none, and the cap keeps a busy thread's events from scanning its whole
// future.
func overlapped(l *bracketList, from int, b agentspool.Bracket, t uint64) bool {
	for k := from; k < min(l.len(), from+overlapScan); k++ {
		if c := l.at(k); c.EnterMono <= t {
			return c.EnterMono > b.EnterMono
		}
	}
	return false
}

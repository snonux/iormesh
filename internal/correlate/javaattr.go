package correlate

import (
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"iormesh/internal/agentspool"
	"iormesh/internal/schema"
)

// kindAliases maps the syscall names of the agent's hook table (the
// x86_64 syscall names, as S1 wrote them) to the event kinds the tracer
// emits, which follow the kernel's tracepoint names.
var kindAliases = map[string]string{
	"stat":  "newstat",
	"lstat": "newlstat",
	"fstat": "newfstat",
}

// eventKinds turns a hook's syscall allowlist into event kinds.
func eventKinds(syscalls []string) []string {
	out := make([]string, len(syscalls))
	for i, s := range syscalls {
		if k, ok := kindAliases[s]; ok {
			s = k
		}
		out[i] = s
	}
	return out
}

// canPark says whether a hook's bracket can contain a park of a virtual
// thread. A hook whose native is its own class's (FileInputStream.read →
// readBytes) wraps the native call alone, during which a virtual thread
// is pinned to its carrier. A hook that reaches another class's native
// (NioSocketImpl.accept → Net.accept, the hook table's qualified
// "owner.name") runs JDK Java code around it, which for a virtual
// thread waits in the poller: it unmounts, its carrier runs other
// virtual threads, and it may resume on the same carrier — which the
// agent's enter/exit tid comparison cannot tell from never leaving.
func canPark(h agentspool.Hook) bool {
	return strings.Contains(h.Native, ".")
}

// attribute joins ev to v's brackets, setting attribution java and the
// java object when one bracket's hook explains it; otherwise ev keeps
// attribution none and the reason is counted. Method and thread table
// rows the event names are written before it.
func (j *JavaJoin) attribute(ev *schema.Event, v *jvm) error {
	mark, marked := j.takeMark(ev)
	if ev.Args.Count != nil {
		j.stats.Ambiguous++ // a folded loop: many calls, maybe many brackets
		return nil
	}
	if !marked {
		// Issued outside every hooked native: JVM work, whatever bracket
		// it falls in and whatever its kind.
		j.stats.Unmarked++
		return nil
	}
	b, ok := j.bracketOf(ev, v, mark)
	if !ok {
		return nil
	}
	stack, err := j.stack(v, b.StackID)
	if err != nil || stack == nil {
		j.stats.NoStack++
		return err
	}
	j.setJava(ev, v, b, stack)
	return nil
}

// bracketOf finds the bracket whose hook explains ev's marked syscall:
// the innermost on ev's tid that contains the native's entry — a stamp
// on the agent's own clock, so no conversion — whose hook reaches that
// native and allows the syscall's kind. A stale mark (a native whose
// return probe never ran) names an entry outside the bracket the syscall
// sits in: the bracket that contains it closed before the syscall
// started, which the syscall's start, converted to the agent's clock,
// shows (a native returns, and its bracket closes, hundreds of ns after
// its last syscall began — S1's smallest margin was 132 ns — while the
// conversion is off by tens). Otherwise the reason is counted.
func (j *JavaJoin) bracketOf(ev *schema.Event, v *jvm, mark NativeMark) (agentspool.Bracket, bool) {
	b, found, ambiguous := v.innermost(int32(ev.TID), mark.EntryMono)
	hook := v.hooks[b.Hook]
	switch {
	case !found || j.mono(ev.BootNS) > b.ExitMono:
		j.stats.Unbracketed++
	case ambiguous || v.mayHaveParked(b, hook):
		j.stats.Ambiguous++
	case mark.Cookie == 0 || mark.Cookie != hook.cookie:
		// Another native inside the bracket (JVM work that called one),
		// a nesting too deep to name the innermost, or a lost HOOK row.
		j.stats.OtherNative++
	case !slices.Contains(hook.kinds, ev.Event):
		// The native's own JVM work (a class loaded inside open0).
		j.stats.OtherKind++
	default:
		return b, true
	}
	return b, false
}

// mayHaveParked: b is a virtual thread's bracket (or one whose THREAD
// record never came) of a hook that can park. Its window on the carrier
// may hold other virtual threads' syscalls — one whose own bracket was
// dropped or moved carriers (tid 0) would land in it — so nothing joins
// it: never a guess, at the price of those hooks' own I/O on virtual
// threads staying none.
func (v *jvm) mayHaveParked(b agentspool.Bracket, h hookInfo) bool {
	th, known := v.threads[b.ThreadID]
	return h.parks && (!known || th.Virtual)
}

// setJava marks ev as joined to b. A virtual thread's I/O is flagged and
// named after its carrier: v1 does not name virtual threads (PLAN.md M3).
func (j *JavaJoin) setJava(ev *schema.Event, v *jvm, b agentspool.Bracket, stack []uint64) {
	th, known := v.threads[b.ThreadID]
	java := &schema.Java{Stack: stack}
	switch {
	case known && th.Virtual:
		java.Carrier = true
		java.Thread = v.names[int32(ev.TID)]
		j.stats.Carrier++
	case known:
		java.Thread = th.Name
	}
	ev.Attribution, ev.Java = schema.AttributionJava, java
	j.stats.Attributed++
}

// stack resolves an agent stack id to method ids, writing the method
// table rows of frames seen for the first time. nil without frames: the
// agent walked no stack (id 0), its STACK record was dropped, or a frame
// does not parse — attribution java needs a stack, so the event stays
// none.
func (j *JavaJoin) stack(v *jvm, id uint64) ([]uint64, error) {
	if ids, ok := v.stacks[id]; ok {
		return ids, nil
	}
	frames := v.frames[id]
	if id == 0 || len(frames) == 0 {
		return nil, nil
	}
	ids := make([]uint64, 0, len(frames))
	for _, f := range frames {
		mid, err := j.method(f)
		if err != nil || mid == 0 {
			return nil, err
		}
		ids = append(ids, mid)
	}
	v.stacks[id] = ids
	return ids, nil
}

// method returns the method id of one frame, writing its table row the
// first time; 0 for a frame that does not parse (no row, no id).
func (j *JavaJoin) method(frame string) (uint64, error) {
	if id, ok := j.methods[frame]; ok {
		return id, nil
	}
	m, err := schema.ParseFrame(frame)
	if err != nil {
		return 0, nil
	}
	m.MethodID = uint64(len(j.methods) + 1)
	if err := j.cfg.Tables.Method(m); err != nil {
		return 0, err
	}
	j.methods[frame] = m.MethodID
	return m.MethodID, nil
}

// MonoOffset samples boot − monotonic time in user space: a monotonic
// read, a boot read, a monotonic read, taking the boot read against the
// midpoint of the two others (S1 experiment 2: within 16–41 ns of an
// in-kernel stamp; the two clocks differ by time spent suspended, so the
// offset is constant between suspends).
func MonoOffset() (int64, error) {
	var m1, b, m2 unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &m1); err != nil {
		return 0, err
	}
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &b); err != nil {
		return 0, err
	}
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &m2); err != nil {
		return 0, err
	}
	return b.Nano() - (m1.Nano()+m2.Nano())/2, nil
}

// addLosses sums two rings' loss counters.
func addLosses(a, b agentspool.Losses) agentspool.Losses {
	a.DroppedBrackets += b.DroppedBrackets
	a.DroppedStacks += b.DroppedStacks
	a.DroppedThreads += b.DroppedThreads
	a.DroppedWatermarks += b.DroppedWatermarks
	a.DroppedHooks += b.DroppedHooks
	a.DroppedHTTP += b.DroppedHTTP
	a.DroppedOther += b.DroppedOther
	a.DroppedBytes += b.DroppedBytes
	a.Abandoned += b.Abandoned
	a.Unknown += b.Unknown
	a.Malformed += b.Malformed
	return a
}

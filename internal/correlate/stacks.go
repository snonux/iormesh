package correlate

import "iormesh/internal/types"

// StackIDs turns the kernel's stack ids into the trace's (M2 step 9,
// -native-stacks): internal/stacks.Table, which also writes the stack
// table. An interface so the correlator stays free of /proc and the BPF
// map, and its tests need neither.
type StackIDs interface {
	// ID returns the trace stack id of kernel stack kid (negative: the
	// kernel's walk failed) in process pid, incarnation inc; ok is false
	// when there is none. The error ends the trace (a failed write).
	ID(pid uint32, inc uint64, kid int32) (id uint32, ok bool, err error)
	// Exec says pid exec'd: its stacks are a new image's from now on.
	Exec(pid uint32)
}

// stackRef is a stack record waiting for the enter it was sent ahead of
// (internal/c/stack.c): the enter's time, and the trace stack id.
type stackRef struct {
	time uint64
	id   uint32
}

// stackRecord resolves a stack record's id and parks it on the thread
// until the enter's exit makes the event (base, takeStack). The enter
// record follows the stack record directly, so one slot per thread is
// enough; a stack whose enter was lost is replaced by the thread's next
// one, or never matched, since the times differ.
func (c *Correlator) stackRecord(r *types.StackEvent) error {
	if c.cfg.Stacks == nil {
		return nil
	}
	c.advance(r.Time)
	id, ok, err := c.cfg.Stacks.ID(r.Pid, c.proc(r.Pid).inc, r.StackId)
	if err != nil || !ok {
		return err
	}
	c.stacks[r.Tid] = stackRef{time: r.Time, id: id}
	return nil
}

// takeStack returns the stack id parked for tid when it belongs to the
// enter stamped enterNS — the stack record carries the enter handler's
// own clock read — and nil otherwise. The slot is emptied either way: the
// thread's enter has been paired, so an unmatched stack is stale.
func (c *Correlator) takeStack(tid uint32, enterNS uint64) *uint32 {
	ref, ok := c.stacks[tid]
	if !ok {
		return nil
	}
	delete(c.stacks, tid)
	if ref.time != enterNS {
		return nil
	}
	return &ref.id
}

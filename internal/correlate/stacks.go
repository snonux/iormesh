package correlate

import "iormesh/internal/types"

// StackIDs turns the kernel's stack ids into the trace's (M2 step 9,
// -native-stacks): internal/stacks.Table, which also writes the stack
// table. An interface so the correlator stays free of /proc and the BPF
// map, and its tests need neither.
type StackIDs interface {
	// ID returns the trace stack id of kernel stack kid (negative: the
	// kernel's walk failed) in process pid, incarnation inc, image tag
	// (the record's mm->arg_start); ok is false when there is none. The
	// error ends the trace (a failed write).
	ID(pid uint32, inc uint64, kid int32, tag uint64) (id uint32, ok bool, err error)
	// InlineID is ID for a stack the kernel sent with its frames (ips,
	// innermost first) because its stack map could not take it (struct
	// stack_inline_event, internal/c/stack.c).
	InlineID(pid uint32, inc uint64, ips []uint64, tag uint64) (id uint32, ok bool, err error)
	// Forget says pid exec'd or exited: the table may drop its state.
	Forget(pid uint32)
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
	id, ok, err := c.cfg.Stacks.ID(r.Pid, c.proc(r.Pid).inc, r.StackId, r.Image)
	return c.parkStack(r.Tid, r.Time, id, ok, err)
}

// stackInlineRecord is stackRecord for a stack sent with its frames: it
// stands in for the stack record of the same enter, so it parks alike.
func (c *Correlator) stackInlineRecord(r *types.StackInlineEvent) error {
	if c.cfg.Stacks == nil {
		return nil
	}
	c.advance(r.Time)
	// Nr comes from the kernel; bound it anyway so a bad record cannot
	// slice past the array.
	ips := r.Ips[:min(int(r.Nr), len(r.Ips))]
	id, ok, err := c.cfg.Stacks.InlineID(r.Pid, c.proc(r.Pid).inc, ips, r.Image)
	return c.parkStack(r.Tid, r.Time, id, ok, err)
}

// parkStack keeps a resolved stack id on thread tid for the enter stamped
// at, or passes the table's error on; a stack without id (ok false) parks
// nothing.
func (c *Correlator) parkStack(tid uint32, at uint64, id uint32, ok bool, err error) error {
	if err != nil || !ok {
		return err
	}
	c.stacks[tid] = stackRef{time: at, id: id}
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

package correlate

import (
	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// Folded reads and writes (M2 step 11, decision #25). The kernel sums a
// thread's hot read or write loop on one fd into one rw_aggregate_event
// (internal/c/rwagg.c) instead of sending each call's enter and exit; this
// file turns such a record into one read or write event whose bytes, ret
// and latency_ns are the calls' sums and whose args.count says how many
// calls it stands for. A record of a single call becomes a plain event
// without args.count: it is indistinguishable from one sent unfolded, and
// a golden's single declared read must match it.
//
// The record can arrive late, and its calls can span a change of the fd
// number: the kernel keys a loop by the fd number (internal/c/rwagg.c), so
// another thread's close, dup2 or reopen of the number while the loop ran
// - or before a blocked thread's record was sent - is invisible to it. The
// record is therefore resolved by time (instanceAt): it names the instance
// that was live from its first call's start to its last call's exit - the
// current one if it was created before the first call, else the one it
// replaced if that covered the whole span. A record no single instance
// explains is an unattributed drop (an explicit loss of its calls, never a
// guess onto the wrong file or connection), counted by Unattributed too.

// pastInstance is the fd instance a number held before its current one
// (or before it was closed): its generation, socket and lifetime on the
// boot clock. One per number is kept, enough for a late record of the
// instance just replaced.
type pastInstance struct {
	gen        uint32
	sock       *sockState
	born, died uint64
}

// aggregate turns one rw_aggregate_event into its read or write event.
func (c *Correlator) aggregate(r *types.RwAggregateEvent) {
	c.advance(r.EndTime)
	p := c.proc(r.Pid)
	gen, sock, ok := p.instanceAt(r.Fd, r.Time, r.EndTime)
	if !ok {
		c.unattributedDrop(p, r)
		return
	}
	en := enter{name: r.TraceId.Name(), shape: shapeFd, time: r.Time, pid: r.Pid, fd: r.Fd}
	ev := c.base(en, r.Tid, int64(r.TotalBytes), r.EndTime)
	ev.FD, ev.FDGen, ev.Bytes, ev.LatencyNS = r.Fd, gen, r.TotalBytes, r.LatencyNs
	if r.Count > 1 {
		count := uint64(r.Count)
		ev.Args.Count = &count
	}
	c.enqueue(ev, sock)
}

// unattributedDrop records a folded record no fd instance explains as an
// explicit loss of its calls (docs/decisions.md #25: any loss is a
// drop event), naming the thread whose calls they were.
func (c *Correlator) unattributedDrop(p *procState, r *types.RwAggregateEvent) {
	c.unattributed++
	lost, reason := uint64(r.Count), schema.DropUnattributed
	ev := c.hostEvent(schema.KindDrop)
	ev.BootNS, ev.WallNS = r.Time, c.wall(r.Time)
	ev.PID, ev.PIDInc, ev.TID, ev.Comm = r.Pid, p.inc, r.Tid, p.comm
	ev.Args.Lost, ev.Args.DropReason = &lost, &reason
	c.enqueue(ev, nil)
}

// Unattributed counts folded read/write records whose calls no single fd
// instance explains (see above); each became an unattributed drop event.
func (c *Correlator) Unattributed() uint64 {
	return c.unattributed
}

// instanceAt returns the generation and socket of the one instance fd
// named from boot time first to last, and false when no single instance
// the trace knows covers that span: the current instance if it was created
// before first, else the instance it replaced if that one lived over the
// whole span. A number the trace never saw created (it predates the trace,
// or an untraced call made it) counts as live since the start, as use()
// has it; so does one closed before first and reused by an untraced call.
func (p *procState) instanceAt(fd int32, first, last uint64) (uint32, *sockState, bool) {
	_, live := p.fds[fd]
	if live && p.born[fd] <= first {
		return p.use(fd), p.sockOf[fd], true
	}
	past, hasPast := p.past[fd]
	if hasPast && past.born <= first && last < past.died {
		return past.gen, past.sock, true
	}
	if !live && (!hasPast || first >= past.died) {
		return p.use(fd), p.sockOf[fd], true
	}
	return 0, nil, false
}

// retire remembers fd's live instance as its past one, ended now: close
// and every creation over a live number call it before they change the
// table.
func (p *procState) retire(fd int32) {
	gen, live := p.fds[fd]
	if !live {
		return
	}
	p.past[fd] = pastInstance{gen: gen, sock: p.sockOf[fd], born: p.born[fd], died: *p.clock}
}

package correlate

import "iormesh/internal/types"

// Folded reads and writes (M2 step 11, decision #25). The kernel sums a
// thread's hot read or write loop on one fd into one rw_aggregate_event
// (internal/c/rwagg.c) instead of sending each call's enter and exit; this
// file turns such a record into one read or write event whose bytes, ret
// and latency_ns are the calls' sums and whose args.count says how many
// calls it stands for. A record of a single call becomes a plain event
// without args.count: it is indistinguishable from one sent unfolded, and
// a golden's single declared read must match it.
//
// The record can arrive late: the kernel flushes a thread's sums only at
// that thread's next flush point, and a thread that folded and then
// blocked in an untraced call may meanwhile see another thread close the
// fd and the number reused. The calls then belong to the instance that was
// live when they ran, not to the number's current one, so the record is
// resolved by time (instanceAt): the current instance if it was created
// before the first folded call, else the instance it replaced if that one
// covered the call. A record neither explains is counted (Unattributed),
// not guessed onto the wrong file or connection.

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
	gen, sock, ok := p.instanceAt(r.Fd, r.Time)
	if !ok {
		c.unattributed++
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

// Unattributed counts folded read/write records whose fd instance could
// not be told: the number was reused more than once between the calls and
// the record (see aggregate.go). They produce no event.
func (c *Correlator) Unattributed() uint64 {
	return c.unattributed
}

// instanceAt returns the generation and socket of the instance fd named at
// boot time at, and false when neither the current instance nor the one it
// replaced was live then. A number the trace never saw created (it
// predates the trace, or an untraced call made it) counts as live since
// the start, as use() has it.
func (p *procState) instanceAt(fd int32, at uint64) (uint32, *sockState, bool) {
	_, live := p.fds[fd]
	if live && p.born[fd] <= at {
		return p.use(fd), p.sockOf[fd], true
	}
	past, hasPast := p.past[fd]
	if hasPast && past.born <= at && at < past.died {
		return past.gen, past.sock, true
	}
	if !live && (!hasPast || at >= past.died) {
		// Never seen, or closed before the calls: the number came from
		// an untraced call, use()'s rule.
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

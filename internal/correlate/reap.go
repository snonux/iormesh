package correlate

// The lifetime of a dead process's state (task xi4). A process whose last
// thread exited (exitTask, group_dead) is marked dead at once: no rename
// or unlink walks its fd table any more, so it gets no opened edge to a
// name renamed after it died. Before, a table kept per dead child — a
// copy of its parent's (inherit) — grew the heap with every fork and
// made every unlink and rename walk it.
//
// The table itself goes once the fuse window has passed since the exit,
// not at it: a group_dead record need not be the process's last. Two
// exiting threads can both report it (el9's signal->live fallback,
// exec.c), and a sibling that left earlier can fire its exit later; that
// sibling's folded read/write records, flushed from its own exit
// (rwagg.c), may trail the first group_dead record and must still find
// their fd instance (instanceAt).
//
// What stays after that is the identity, inc and comm, for one kind of
// late record: the kernel reports a socket's last transitions after its
// owner died (tcp_state: the FIN_WAIT2 or LAST_ACK tail, and the
// session-end final snapshot of a socket still open), owned by the dead
// pid, and those must name that incarnation. Their fd generation and conn
// id live on the socket (sockState.fdGen), not in the table. So the
// identity is kept while any TCP socket the process owns is still in the
// socket table, and deleted once none is and the fuse window has passed
// since the later of the exit and the last socket's close — also a margin
// for records that cross CPUs on their way. A socket whose TCP_CLOSE
// record was lost, and whose address the kernel never reuses (which would
// replace it, observe), keeps its dead owner's identity record — no fd
// table, a few hundred bytes — for the session: bounded by such losses,
// which drop events already count. A later process under the same pid
// replaces the state as before (newTask, Seed, Incarnation); the reaper
// then finds another state under the pid and leaves it alone.

// dying is a dead process waiting out its fuse window, which began at
// (boot ns): at its exit or its last socket's close.
type dying struct {
	p  *procState
	at uint64
}

// markDead marks p dead at its first group-dead record and queues it for
// reaping. A duplicate group-dead record (two last threads, exec.c)
// changes nothing.
func (c *Correlator) markDead(p *procState) {
	if p.dead {
		return
	}
	p.dead = true
	c.queueDying(p)
}

// queueDying starts p's fuse window now. Only the latest entry of a
// process counts (procState.dyingAt): one queued at its exit, before a
// late first record made it own a socket, must not reap it inside the
// window after that socket's close.
func (c *Correlator) queueDying(p *procState) {
	p.dyingAt = c.now
	c.dying = append(c.dying, dying{p: p, at: c.now})
}

// own makes p the process socket s's records name: the one its binding
// record (sockGen: a connect's first transition, an accept snapshot)
// carried.
func (c *Correlator) own(s *sockState, p *procState) {
	if s.owner == p {
		return
	}
	c.disown(s)
	s.owner = p
	p.socks++
}

// disown ends s's ownership: the socket left the socket table (closed, or
// replaced by a new life at its address). A dead owner's last socket
// starts its fuse window anew.
func (c *Correlator) disown(s *sockState) {
	o := s.owner
	if o == nil {
		return
	}
	s.owner = nil
	o.socks--
	if o.dead && o.socks == 0 {
		c.queueDying(o)
	}
}

// disownGone disowns the sockets one tcp_state record took out of the
// socket table: old, the socket at sk before the record, when a new life
// replaced it, and s itself when the record closed it.
func (c *Correlator) disownGone(sk uint64, old, s *sockState) {
	if old != nil && old != s {
		c.disown(old)
	}
	if c.socks.bySk[sk] != s {
		c.disown(s)
	}
}

// reap settles the dead processes whose fuse window has passed. dying is
// in time order (c.now never goes back), so the first entry still inside
// its window ends the walk. A stale entry (a later one of its process
// exists) is skipped. Otherwise the process's fd table goes, and so does
// its whole state unless it owns a socket again (a late first record:
// that socket's close queues it anew) or its pid names a successor.
func (c *Correlator) reap() {
	n := 0
	for ; n < len(c.dying); n++ {
		d := c.dying[n]
		if c.now < d.at+c.cfg.FuseWindowNS {
			break
		}
		if d.at != d.p.dyingAt {
			continue
		}
		c.reapOne(d.p)
	}
	if n > 0 {
		clear(c.dying[:n]) // the array holds no reaped state for the GC
		c.dying = c.dying[n:]
	}
}

// reapOne drops a dead process's fd table and, when nothing may still
// name it, its state.
func (c *Correlator) reapOne(p *procState) {
	if !p.fdsDropped {
		p.fdsDropped = true
		p.resetFds()
	}
	if p.socks != 0 || c.procs[p.pid] != p {
		return
	}
	delete(c.procs, p.pid)
	if c.lastProc == p {
		c.lastPID, c.lastProc = 0, nil
	}
}

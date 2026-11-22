package correlate

// The lifetime of a dead process's state (task xi4). A process whose last
// thread exited (exitTask, group_dead) keeps nothing of its fd table: the
// kernel closed every fd, no record of the process can name one again,
// and a table kept per dead child — a copy of its parent's (inherit) —
// grew the heap with every fork and made every unlink and rename walk it,
// giving dead processes opened edges to names renamed after they died.
//
// What stays is the identity, inc and comm, for one kind of late record:
// the kernel reports a socket's last transitions after its owner died
// (tcp_state: the FIN_WAIT2 or LAST_ACK tail, and the session-end final
// snapshot of a socket still open), owned by the dead pid, and those must
// name that incarnation. Their fd generation and conn id live on the
// socket (sockState.fdGen), not in the table. So the identity is kept
// while any TCP socket the process owns is still in the socket table, and
// deleted once none is and the fuse window has passed since the later of
// the exit and the last socket's close — a margin for records that cross
// CPUs on their way. A later process under the same pid replaces the
// state as before (newTask, Seed, Incarnation); the reaper then finds
// another state under the pid and leaves it alone.

// dying is a dead process waiting out its fuse window: since at (boot
// ns), none of its sockets was open.
type dying struct {
	p  *procState
	at uint64
}

// dropFds forgets a dead process's fd table and queues its state for
// reaping, or leaves that to its last socket's close (disown). A
// duplicate group-dead record (two last threads, exec.c) changes nothing.
func (c *Correlator) dropFds(p *procState) {
	if p.dead {
		return
	}
	p.dead = true
	p.resetFds()
	if p.socks == 0 {
		c.dying = append(c.dying, dying{p: p, at: c.now})
	}
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
// starts its fuse window.
func (c *Correlator) disown(s *sockState) {
	o := s.owner
	if o == nil {
		return
	}
	s.owner = nil
	o.socks--
	if o.dead && o.socks == 0 {
		c.dying = append(c.dying, dying{p: o, at: c.now})
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

// reap deletes the dead processes whose fuse window has passed with no
// socket open. dying is in time order (c.now never goes back), so the
// first entry still inside its window ends the walk. An entry whose
// process owns a socket again (a late first record) or whose pid now
// names a successor is dropped without deleting anything: the socket's
// close queues it anew, and the successor is alive.
func (c *Correlator) reap() {
	n := 0
	for ; n < len(c.dying); n++ {
		d := c.dying[n]
		if c.now < d.at+c.cfg.FuseWindowNS {
			break
		}
		if d.p.socks != 0 || c.procs[d.p.pid] != d.p {
			continue
		}
		delete(c.procs, d.p.pid)
		if c.lastProc == d.p {
			c.lastPID, c.lastProc = 0, nil
		}
	}
	if n > 0 {
		clear(c.dying[:n]) // the array holds no reaped state for the GC
		c.dying = c.dying[n:]
	}
}

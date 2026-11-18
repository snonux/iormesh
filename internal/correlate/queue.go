package correlate

import "iormesh/internal/schema"

// queued is an event waiting to be emitted. A socket event waits until its
// socket's conn id is known, and every event behind it waits too, so the
// sink sees seq order.
type queued struct {
	ev   schema.Event
	sock *sockState
}

// enqueue numbers ev and appends it. Seq is assigned here, in completion
// order, so it stays gap-free whatever the queue holds back.
func (c *Correlator) enqueue(ev schema.Event, sock *sockState) {
	c.seq++
	ev.Seq = c.seq
	c.queue = append(c.queue, queued{ev: ev, sock: sock})
}

// release emits queued events from the head while each is ready: not a
// socket event, or one whose conn id is known, or one whose fuse window
// has passed (it then becomes unresolved). force releases everything.
func (c *Correlator) release(force bool) error {
	n := 0
	for ; n < len(c.queue); n++ {
		q := c.queue[n]
		if q.sock != nil && q.sock.connID == "" {
			if !force && c.now < q.sock.deadline(c.cfg.FuseWindowNS) {
				break
			}
			c.resolverOf(q.sock).resolveAlone(q.sock)
		}
		if err := c.emit(q); err != nil {
			c.queue = c.queue[n+1:]
			return err
		}
	}
	c.queue = c.queue[n:]
	return nil
}

func (c *Correlator) emit(q queued) error {
	ev := q.ev
	if q.sock != nil {
		ev.ConnID = q.sock.connID
		c.graph.connected(ev.PID, ev.PIDInc, q.sock.connID, q.sock.fused)
	}
	return c.cfg.Sink.Event(ev)
}

// resolverOf is the table that pairs s: its own, or the TCP table.
func (c *Correlator) resolverOf(s *sockState) resolver {
	if s.table != nil {
		return s.table
	}
	return c.socks
}

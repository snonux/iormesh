package correlate

import (
	"fmt"
	"slices"
)

// The end of a JVM's ring (task 2j4). A JVM that exited used to stay in
// the join for the session: its ring mapped and drained every poll, its
// last Retain of brackets, its frames, stacks and classes kept, and its
// file — 8 MiB by default, on the tmpfs under /run — left in the spool.
// Now the session reports each traced process's group-dead exit
// (Exited), and once nothing can ask for the ring any more, retire reads
// it a last time, closes it, removes its file (agentspool.Ring.Remove,
// which unlinks only the inode it mapped) and forgets the JVM.
//
// "Nothing can ask" is two conditions. Every event of the process must
// have reached the join: the exit record is handled as it arrives, while
// the correlator may still hold the process's earlier socket events, and
// the whole stream behind them, for its fuse window (UpstreamHold); and
// an event of the JVM that waits for a watermark leaves at the latest
// Timeout after its end. So the boot clock must be UpstreamHold + Timeout
// past the exit — the bound httpReady uses for the same reason. And no
// event of the JVM may still be held: one stuck behind another JVM's
// event in seq order is decided with this JVM's brackets, later.
//
// What was read stays where it was copied: http records still queued
// keep their JVM by pointer, and the method, class and thread tables
// were written already. A ring found but never traced whose process
// exited (its pid's exit reached the session, but none of its events
// the join) is closed and removed the same way.

// Exited reports that pid's thread group died at boot time at (the
// group-dead exit record): the ring of that process, accepted or
// waiting, is retired once nothing can ask for it. A ring whose process
// started after at is a later process under the pid and stays. The
// session calls it for every group-dead exit it handles, before the
// record reaches the correlator.
func (j *JavaJoin) Exited(pid uint32, at uint64) {
	mark := func(v *jvm) {
		if v == nil || v.exited != 0 || v.start > at {
			return
		}
		v.exited = max(at, 1)
		j.exiting++
	}
	mark(j.jvms[pid])
	for _, v := range j.waiting {
		if v.pid == pid {
			mark(v)
		}
	}
}

// retire closes and removes the rings of exited processes that nothing
// can ask for any more (see the top of this file). An error is a table
// error from the last drain, which ends the trace.
func (j *JavaJoin) retire() error {
	if j.exiting == 0 {
		return nil
	}
	j.exiting = 0
	for pid, v := range j.jvms {
		if v.exited == 0 {
			continue
		}
		if !j.retirable(v) {
			j.exiting++
			continue
		}
		if err := j.drain(v); err != nil {
			return err
		}
		j.closeRing(v, true)
		delete(j.jvms, pid)
		j.untrace(v)
	}
	j.waiting = slices.DeleteFunc(j.waiting, func(v *jvm) bool {
		switch {
		case v.exited == 0:
			return false
		case j.scanned < v.exited+j.cfg.UpstreamHold+j.cfg.Timeout:
			j.exiting++
			return false
		}
		j.removeRing(v)
		if err := v.ring.Close(); err != nil {
			j.warn(fmt.Errorf("java ring, pid %d: close: %w", v.pid, err))
		}
		return true
	})
	return nil
}

// retirable: the clock is past every event of v's process that may still
// come, and none of them is held.
func (j *JavaJoin) retirable(v *jvm) bool {
	if j.scanned < v.exited+j.cfg.UpstreamHold+j.cfg.Timeout {
		return false
	}
	for i := j.head; i < len(j.held); i++ {
		if ev := &j.held[i]; ev.PID == v.pid && v.owns(ev) {
			return false
		}
	}
	return true
}

// closeRing counts an accepted ring's losses and closes it, removing its
// file too when remove says its process is dead. A failure is warned
// about, not fatal: the trace is complete without the ring.
func (j *JavaJoin) closeRing(v *jvm, remove bool) {
	j.stats.Lost = addLosses(j.stats.Lost, v.ring.Losses())
	if remove {
		j.removeRing(v)
	}
	if err := v.ring.Close(); err != nil {
		j.warn(fmt.Errorf("java ring, pid %d: close: %w", v.pid, err))
	}
}

// removeRing unlinks v's ring file, warning when that fails.
func (j *JavaJoin) removeRing(v *jvm) {
	if err := v.ring.Remove(); err != nil {
		j.warn(fmt.Errorf("java ring, pid %d: remove: %w", v.pid, err))
	}
}

// untrace forgets that the stream named v's incarnation: it is dead, and
// traced would otherwise keep one entry per JVM the session saw. A late
// event of it names it again, harmlessly: its ring is gone.
func (j *JavaJoin) untrace(v *jvm) {
	incs := slices.DeleteFunc(j.traced[v.pid], func(inc uint64) bool { return inc == v.start })
	if len(incs) == 0 {
		delete(j.traced, v.pid)
	} else {
		j.traced[v.pid] = incs
	}
	if j.lastPID == v.pid && j.lastInc == v.start {
		j.lastPID, j.lastInc = 0, 0
	}
}

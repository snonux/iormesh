package correlate

import (
	"fmt"
	"slices"

	"iormesh/internal/schema"
)

// Which rings the join reads (task yi4). The spool (decision #27) is
// writable by every user, and its scan (agentspool.Dir) checks only that
// a ring's file belongs to the user of the process its name names. That
// keeps a user from speaking for another user's process, not from
// speaking for a process the session does not trace: a ring of any
// process of one's own — a forged one, or a JVM outside the target that
// inherited JAVA_TOOL_OPTIONS — would put http and dns events and table
// rows into the trace and have the root tracer parse that process's
// libraries and attach uprobes to it.
//
// So a ring is read only once its process is traced, and the stream says
// which processes are: every event that reaches the join is of a traced
// process — the kernel filters by the session's targets, and each one
// the session takes on, or follows into a fork, gets an attach or fork
// event first — save the host's own events (session, end, a host-wide
// drop), which name no pid. A ring whose process the stream has not
// named (yet) waits unread, neither drained nor probed nor emitted: the
// agent loaded at exec by JAVA_TOOL_OPTIONS maps its ring before the
// session's adoption of the exec may have passed the correlator, and that
// ring must still be read once it has. The ring buffers what the agent
// writes meanwhile, as it does for a lagging reader.
//
// Traced means the very incarnation: the pid and the start time from
// /proc, equal to an event's pid_incarnation, so a ring of a process
// that reused a traced one's pid stays unread — and so does a ring whose
// process's start time could not be read, never matched by the ring's
// own (writer-chosen) creation stamp. Only the newest waiting ring of a
// pid is kept.

// trace notes ev's process as traced and reads the rings that waited
// for it. A run of one process's events costs a comparison.
func (j *JavaJoin) trace(ev *schema.Event) {
	if ev.PID == 0 || (ev.PID == j.lastPID && ev.PIDInc == j.lastInc) {
		return
	}
	j.lastPID, j.lastInc = ev.PID, ev.PIDInc
	incs := j.traced[ev.PID]
	if slices.Contains(incs, ev.PIDInc) {
		return
	}
	j.traced[ev.PID] = append(incs, ev.PIDInc)
	j.adoptWaiting()
}

// isTraced reports whether the stream has named v's process: its start
// time from /proc, read when the ring was added, is an incarnation an
// event carried. A ring whose start time is unknown (its process gone
// between the scan's owner check and addRing, or /proc unreadable) is
// never trusted: jvm.owns would fall back to the ring's creation stamp,
// which the ring's writer chooses (task yi4 review). The spool's scan
// refuses rings of processes already gone for the same reason.
func (j *JavaJoin) isTraced(v *jvm) bool {
	return v.start != 0 && slices.Contains(j.traced[v.pid], v.start)
}

// hold puts v among the waiting rings. An older ring of the same pid
// still waiting is closed and dropped: the newer one is the pid's
// current file (the agent replaces a stale ring, never reuses it), and
// a writer re-planting <pid>.ring under new inodes must not make every
// scan map one more (task yi4 review).
func (j *JavaJoin) hold(v *jvm) {
	for i, w := range j.waiting {
		if w.pid == v.pid {
			if err := w.ring.Close(); err != nil {
				j.warn(fmt.Errorf("java ring, pid %d: close: %w", w.pid, err))
			}
			j.waiting[i] = v
			return
		}
	}
	j.waiting = append(j.waiting, v)
}

// adoptWaiting accepts the waiting rings whose process is now traced, in
// the order they were found, so a later ring of the pid still replaces an
// earlier one. Accepted here, a ring is drained at the next poll; an event
// of its process that waits for it meanwhile is held (pending), since the
// ring now exists and its watermark has not passed.
func (j *JavaJoin) adoptWaiting() {
	if len(j.waiting) == 0 {
		return
	}
	j.waiting = slices.DeleteFunc(j.waiting, func(v *jvm) bool {
		if !j.isTraced(v) {
			return false
		}
		j.accept(v)
		return true
	})
}

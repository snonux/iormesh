package correlate

import (
	"fmt"
	"net/netip"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"iormesh/internal/target"
)

// procState is one traced process incarnation.
type procState struct {
	inc  uint64
	comm string
	// fds holds the live fd instances; lastGen remembers each number's
	// newest generation after close too, so a number reused by a syscall
	// M1 does not trace (socket, dup, pipe, …) still gets a fresh fd_gen.
	fds     map[int32]uint32
	lastGen map[int32]uint32
	// sockOf binds an fd instance to the socket it names; tcp_state
	// records carry that binding. It holds the socket itself, not its sk
	// address: the sk is dropped from the socket table at TCP_CLOSE (the
	// address may be reused), while the fd — after an RST, say — stays
	// open and its events still belong to that connection.
	sockOf map[int32]*sockState
	// paths holds the current path of each fd instance a traced open (or
	// a dup of one) named, rewritten by renames (paths.go). It names the
	// dirfd of a relative *at call and the file of an fd metadata event.
	paths map[int32]fdPath
	// cloexec is the close-on-exec state of the fd instances the trace
	// knows it of (fdtable.go): true set, false clear, absent unknown.
	cloexec map[int32]bool
	// flowsOf lists the UDP flows (by key) each fd started (ipc.go).
	flowsOf map[int32][][2]netip.AddrPort
	// born is when each live instance a traced call created was created
	// (the creating record's boot time), past each number's previous
	// instance: a folded read/write record can arrive after its fd was
	// closed and reused, and is matched to its instance by time
	// (aggregate.go). clock points at the correlator's current record
	// time, which stamps both.
	born  map[int32]uint64
	past  map[int32]pastInstance
	clock *uint64
}

func (c *Correlator) proc(pid uint32) *procState {
	if p, ok := c.procs[pid]; ok {
		return p
	}
	comm, inc := c.cfg.ProcInfo(pid)
	p := &procState{inc: inc, comm: comm, fds: map[int32]uint32{},
		lastGen: map[int32]uint32{}, sockOf: map[int32]*sockState{},
		paths: map[int32]fdPath{}, cloexec: map[int32]bool{},
		flowsOf: map[int32][][2]netip.AddrPort{}, born: map[int32]uint64{},
		past: map[int32]pastInstance{}, clock: &c.recTime}
	c.procs[pid] = p
	c.graph.process(pid, inc, comm)
	return p
}

// create registers a new fd instance returned by a traced syscall and
// returns its generation. A non-socket creation (open, dup) drops any
// stale socket binding of the number; accept keeps the one its snapshot
// record set just before. The new instance has no path and no known
// close-on-exec state until its creator sets them: the number's old ones
// belonged to the instance it replaces, which is retired as the number's
// past instance (aggregate.go).
func (p *procState) create(fd int32, socket bool) uint32 {
	p.retire(fd)
	gen := p.lastGen[fd] + 1
	p.lastGen[fd] = gen
	p.fds[fd] = gen
	p.born[fd] = *p.clock
	if !socket {
		delete(p.sockOf, fd)
	}
	delete(p.paths, fd)
	delete(p.cloexec, fd)
	return gen
}

// use returns the generation of an fd a syscall acts on. An fd never seen
// predates the trace (gen 0); one seen and closed before was recreated by
// an untraced syscall, so it is a new instance (gen + 1).
func (p *procState) use(fd int32) uint32 {
	if gen, ok := p.fds[fd]; ok {
		return gen
	}
	gen := uint32(0)
	if last, ok := p.lastGen[fd]; ok {
		gen = last + 1
	}
	p.lastGen[fd] = gen
	p.fds[fd] = gen
	return gen
}

// nextGen is the generation the next creation of fd will get, for the
// accept snapshot that arrives just before its accept exit.
func (p *procState) nextGen(fd int32) uint32 {
	return p.lastGen[fd] + 1
}

// sockGen binds fd to socket sk on the socket's first record and returns
// the fd generation a tcp_state event reports:
//   - the accept snapshot precedes the accept exit that creates the fd, so
//     it reports the generation that creation will get (use() would
//     register, and maybe bump, the number before it exists);
//   - the first record of a connecting socket (CLOSE -> SYN_SENT, inside
//     connect) binds the fd the connect acts on;
//   - later transitions report the instance bound then (kept on the
//     socket), even once the fd was closed (TCP_CLOSE often follows
//     close(2)) — never re-binding a number another open may reuse.
//
// If the accept exit after a snapshot is lost, the fd is never created:
// the snapshot reported last+1 while later uses of the number report the
// use() rule's generation. The two then disagree; a drop event already
// marks that gap.
func (p *procState) sockGen(fd int32, s *sockState, first, snapshot bool) uint32 {
	switch {
	case snapshot:
		p.sockOf[fd] = s
		s.fdGen = p.nextGen(fd)
	case first:
		p.sockOf[fd] = s
		s.fdGen = p.use(fd)
	}
	return s.fdGen
}

// close forgets a released fd instance. lastGen keeps the number's
// generation, so whatever reuses the number is a new instance; past keeps
// the instance itself for a folded record that arrives late (retire).
func (p *procState) close(fd int32) {
	p.retire(fd)
	delete(p.born, fd)
	delete(p.fds, fd)
	delete(p.sockOf, fd)
	delete(p.paths, fd)
	delete(p.cloexec, fd)
}

// ReadProcInfo reads comm and start time from /proc. The start time has
// clock-tick resolution, which is enough to tell PID reuses apart. A
// process already gone yields ("", 0): its events still carry its pid.
func ReadProcInfo(pid uint32) (string, uint64) {
	comm, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return strings.TrimSpace(string(comm)), 0
	}
	return strings.TrimSpace(string(comm)), parseStartTime(stat)
}

// parseStartTime returns field 22 (starttime) of a /proc/<pid>/stat line
// in boot-clock nanoseconds, 0 for a line that does not parse
// (target.ParseStat, shared with the session's liveness check).
func parseStartTime(stat []byte) uint64 {
	st, err := target.ParseStat(stat)
	if err != nil {
		return 0
	}
	return st.StartNS
}

// BootToWallOffset measures wall-clock minus boot-clock time now, the
// Config.WallOffsetNS that converts BPF timestamps (bpf_ktime_get_boot_ns)
// to wall time. Measured once per session: the offset drifts only when the
// wall clock is stepped, and a stepped clock must not reorder events.
func BootToWallOffset() (int64, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err != nil {
		return 0, err
	}
	return time.Now().UnixNano() - ts.Nano(), nil
}

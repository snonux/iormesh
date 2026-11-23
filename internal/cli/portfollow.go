package cli

import (
	"fmt"
	"time"

	"iormesh/internal/target"
)

// A -listen-port session follows its port across restarts of the program
// that owns it (PLAN.md decision #23, task mh4): every host expand -apply
// enrolls is bound by port, and ending at the app's first restart lost
// the host's trace.
const (
	// defaultListenGrace is how long a port session whose targets are
	// all gone waits for a new owner before it ends (-listen-grace): a
	// service restart — a stop, a RestartSec pause, a JVM start up to
	// its bind — fits in it.
	defaultListenGrace = 2 * time.Minute
	// portFollowEvery is how often the waiting session re-resolves the
	// port: a resolve reads every process's fd table.
	portFollowEvery = time.Second
)

// listenOwners resolves the port's owners, each with the start time the
// listing saw (target.ProcFS.ListenHolders, never a service manager);
// procOwner reads a process's owner key
// (target.ProcFS.Owner). Variables like processAlive, so tests need no
// /proc.
var (
	listenOwners = func(port uint16) ([]target.Holder, error) { return target.HostProcFS().ListenHolders(port) }
	procOwner    = func(pid uint32) (target.Owner, error) { return target.HostProcFS().Owner(pid) }
)

// portFollow is a -listen-port session's wait for a restarted owner.
type portFollow struct {
	port  uint16
	grace time.Duration
	// owners are the keys (executable, effective uid, cgroup) of the
	// processes the session started with that run the original holder's
	// executable (remember): a later holder of the port is adopted only
	// if it is the same app as one of them (target.Owner.SameApp: same
	// uid and executable, or task kk4, an upgraded path of the same base
	// name in the same cgroup). Empty when none could be read — then
	// nothing is recognised and the session ends as before.
	owners map[target.Owner]bool
	// since is when the last target went, zero while one is traced; last
	// is the last resolve. now is the clock, a field so tests set it.
	since, last time.Time
	now         func() time.Time
	// refused are the holders already reported as not the app, so a
	// stranger on the port is named once, not every second.
	refused map[uint32]bool
}

func newPortFollow(cfg runConfig) *portFollow {
	return &portFollow{port: cfg.spec.ListenPort, grace: cfg.listenGrace,
		owners: map[target.Owner]bool{}, refused: map[uint32]bool{}, now: time.Now}
}

// remember records the owner keys of the processes the session starts
// with. One that cannot be read is no key: its restart is not followed.
// Only keys of the original holder's executable count (task qj4): the
// original is the earliest-started holder, the one that most likely
// bound the port. Another program that merely inherited the listener fd
// (a root helper) would otherwise widen which programs a later adoption
// accepts. The uid is not restricted: a privilege-separated server
// (nginx, httpd: a root master, workers of the same binary under their
// own uid, all holding the listener) must have its restarted workers
// adopted too, since they fork before the next re-resolve and
// fork-following never picks up a process that already exists. Only
// start holders are remembered, never adopted ones: a chain of upgrades
// (25.0.5, 25.0.6, 25.0.7) still matches through the original's base
// name and cgroup (task kk4).
func (f *portFollow) remember(pids []uint32) {
	keys := map[uint32]target.Owner{}
	var first uint64
	var exe string
	for _, pid := range pids {
		o, err := procOwner(pid)
		start := processStart(pid)
		if err != nil || start == 0 {
			continue
		}
		keys[pid] = o
		if first == 0 || start < first {
			first, exe = start, o.Exe
		}
	}
	for _, o := range keys {
		if o.Exe == exe {
			f.owners[o] = true
		}
	}
}

// ended reports whether the session is done: over() says nothing is left
// to trace, and a port session also found no new owner within its grace
// (followPort). Only a port session waits; a pattern session is never
// over, a PID session ends at once.
func (s *session) ended() (bool, error) {
	if !s.over() {
		if s.follow != nil {
			s.follow.since = time.Time{}
		}
		return false, nil
	}
	if s.follow == nil {
		return true, nil
	}
	return s.followPort()
}

// recognises reports whether o, a new holder's owner key, is the app of
// one of the remembered owners (target.Owner.SameApp, decision #23).
func (f *portFollow) recognises(o target.Owner) bool {
	for prev := range f.owners {
		if o.SameApp(prev) {
			return true
		}
	}
	return false
}

// followPort runs while a port session has no live target: once per
// portFollowEvery it resolves the port and adopts each holder that is the
// session's program again (adoptOwner). It ends the session, saying why
// on stderr, when the grace ran out — at once for -listen-grace 0 — or
// when no owner key was read at start, so nothing could be recognised.
func (s *session) followPort() (bool, error) {
	f := s.follow
	now := f.now()
	if f.since.IsZero() {
		f.since = now
	}
	switch {
	case f.grace == 0:
		return true, nil
	case len(f.owners) == 0:
		_, _ = fmt.Fprintf(s.log, "iormesh: port %d: the owner's executable was unreadable at start; "+
			"not following the port\n", f.port)
		return true, nil
	case now.Sub(f.since) >= f.grace:
		_, _ = fmt.Fprintf(s.log, "iormesh: port %d: no new owner within %v\n", f.port, f.grace)
		return true, nil
	case !f.last.IsZero() && now.Sub(f.last) < portFollowEvery:
		return false, nil
	}
	f.last = now
	holders, err := listenOwners(f.port)
	if err != nil {
		return false, nil // nothing (visible) listens yet: keep waiting
	}
	for _, h := range holders {
		if err := s.adoptOwner(h); err != nil {
			return false, err
		}
	}
	if len(s.live) > 0 {
		f.since = time.Time{}
	}
	return false, nil
}

// adoptOwner traces h, a holder of the port, if its owner key is one
// the session started with. Like the start scan's adoptRunning it reads
// the identity first and re-checks liveness after the insert (addRunning),
// then seeds the correlator, which writes the process's attach event —
// a new incarnation of the port's app — and with -agent tries to load
// the agent into it (task lh4). The identity must have the start
// time the listing saw (task qj4): a pid reused since by a process that
// does not hold the port is skipped — the next resolve lists whoever
// holds it — and addRunning's re-check ties the owner key read in
// between to that same incarnation. A holder of another program, user
// or — for an upgraded executable path — cgroup (recognises) is named
// once on stderr and left alone.
func (s *session) adoptOwner(h target.Holder) error {
	f, pid := s.follow, h.PID
	id := attachIdentity(pid)
	if id.StartNS == 0 || id.StartNS != h.StartNS {
		return nil // gone already, or no longer the listed holder
	}
	if o, err := procOwner(pid); err != nil || !f.recognises(o) {
		if !f.refused[pid] {
			f.refused[pid] = true
			_, _ = fmt.Fprintf(s.log, "iormesh: port %d: pid %d holds it but is not the traced program "+
				"(executable, uid or cgroup differs); not following\n", f.port, pid)
		}
		return nil
	}
	added, err := s.addRunning(pid, id.StartNS)
	if err != nil || !added {
		return err
	}
	s.live[pid] = id.StartNS
	_, _ = fmt.Fprintf(s.log, "iormesh: port %d: following new owner pid %d\n", f.port, pid)
	s.late.exec(pid)
	return s.c.Seed(pid, id)
}

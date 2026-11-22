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

// listenOwners resolves the port's owners (target.ProcFS.ListenOwners,
// never a service manager); procOwner reads a process's owner key
// (target.ProcFS.Owner). Variables like processAlive, so tests need no
// /proc.
var (
	listenOwners = func(port uint16) ([]uint32, error) { return target.HostProcFS().ListenOwners(port) }
	procOwner    = func(pid uint32) (target.Owner, error) { return target.HostProcFS().Owner(pid) }
)

// portFollow is a -listen-port session's wait for a restarted owner.
type portFollow struct {
	port  uint16
	grace time.Duration
	// owners are the keys (executable, effective uid) of the processes
	// the session started with: a later holder of the port is adopted
	// only if it is one of them again. Empty when none could be read —
	// then nothing is recognised and the session ends as before.
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
func (f *portFollow) remember(pids []uint32) {
	for _, pid := range pids {
		if o, err := procOwner(pid); err == nil {
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
	pids, err := listenOwners(f.port)
	if err != nil {
		return false, nil // nothing (visible) listens yet: keep waiting
	}
	for _, pid := range pids {
		if err := s.adoptOwner(pid); err != nil {
			return false, err
		}
	}
	if len(s.live) > 0 {
		f.since = time.Time{}
	}
	return false, nil
}

// adoptOwner traces pid, a holder of the port, if its owner key is one
// the session started with. Like the start scan's adoptRunning it reads
// the identity first and re-checks liveness after the insert (addRunning),
// then seeds the correlator, which writes the process's attach event —
// a new incarnation of the port's app. A holder of another program or
// user is named once on stderr and left alone.
func (s *session) adoptOwner(pid uint32) error {
	f := s.follow
	id := attachIdentity(pid)
	if id.StartNS == 0 {
		return nil // gone already
	}
	if o, err := procOwner(pid); err != nil || !f.owners[o] {
		if !f.refused[pid] {
			f.refused[pid] = true
			_, _ = fmt.Fprintf(s.log, "iormesh: port %d: pid %d holds it but is not the traced program "+
				"(executable or uid differs); not following\n", f.port, pid)
		}
		return nil
	}
	added, err := s.addRunning(pid, id.StartNS)
	if err != nil || !added {
		return err
	}
	s.live[pid] = id.StartNS
	_, _ = fmt.Fprintf(s.log, "iormesh: port %d: following new owner pid %d\n", f.port, pid)
	return s.c.Seed(pid, id)
}

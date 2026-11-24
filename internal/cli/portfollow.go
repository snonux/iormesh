package cli

import (
	"errors"
	"fmt"
	"time"

	"iormesh/internal/correlate"
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
// procOwner reads a process's owner key (target.ProcFS.Owner);
// managerListener pins the listener of a port only a service manager
// holds and activatedHolders lists who holds it since (task lk4,
// target.ProcFS.ManagerListener and ActivatedHolders). Variables like
// processAlive, so tests need no /proc.
var (
	listenOwners     = func(port uint16) ([]target.Holder, error) { return target.HostProcFS().ListenHolders(port) }
	procOwner        = func(pid uint32) (target.Owner, error) { return target.HostProcFS().Owner(pid) }
	managerListener  = func(port uint16) (target.Activation, error) { return target.HostProcFS().ManagerListener(port) }
	activatedHolders = func(a target.Activation) (adopt, refuse []target.Holder, err error) {
		return target.HostProcFS().ActivatedHolders(a)
	}
)

// portActivation decides what a -listen-port session does when resolving
// its port at start failed with err (decision #23, task lk4). A port only
// the service manager holds — an idle socket-activated service — with a
// grace to wait in gets its listener pinned: the session starts with no
// target and waits for the activated service (awaitActivation). Any
// other error, -listen-grace 0 or a listener that cannot be pinned keeps
// the refusal.
func portActivation(cfg runConfig, err error) (*target.Activation, error) {
	if !errors.Is(err, target.ErrManagerOnly) || cfg.listenGrace == 0 {
		return nil, err
	}
	a, perr := managerListener(cfg.spec.ListenPort)
	if perr != nil {
		return nil, errors.Join(err, perr)
	}
	return &a, nil
}

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
	// activation is the listener pinned when only the service manager
	// held the port at start (portActivation, task lk4): until its
	// activated service is adopted the session has no owner key, and
	// awaitActivation stands in for the follow. nil otherwise, and once
	// the service is adopted and remembered.
	activation *target.Activation
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
// name and cgroup (task kk4). The one exception is a session that
// started on a port only the service manager held (task lk4): it has no
// start holder, so the socket-activated service it adopts first is
// remembered in their place (awaitActivation).
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
// session's program again (adoptOwner) — or, while the session waits for
// a socket-activated service, the service (awaitActivation). It ends the
// session, saying why on stderr, when the grace ran out — at once for
// -listen-grace 0 — or when no owner key was read at start, so nothing
// could be recognised.
func (s *session) followPort() (bool, error) {
	f := s.follow
	now := f.now()
	if f.since.IsZero() {
		f.since = now
	}
	switch {
	case f.grace == 0:
		return true, nil
	case len(f.owners) == 0 && f.activation == nil:
		_, _ = fmt.Fprintf(s.log, "iormesh: port %d: the owner's executable was unreadable at start; "+
			"not following the port\n", f.port)
		return true, nil
	case now.Sub(f.since) >= f.grace && f.activation != nil:
		_, _ = fmt.Fprintf(s.log, "iormesh: port %d: no socket-activated service started within %v\n",
			f.port, f.grace)
		return true, nil
	case now.Sub(f.since) >= f.grace:
		_, _ = fmt.Fprintf(s.log, "iormesh: port %d: no new owner within %v\n", f.port, f.grace)
		return true, nil
	case !f.last.IsZero() && now.Sub(f.last) < portFollowEvery:
		return false, nil
	}
	f.last = now
	if f.activation != nil {
		return false, s.awaitActivation()
	}
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
		s.refuseHolder(pid, "is not the traced program (executable, uid or cgroup differs)")
		return nil
	}
	_, err := s.traceHolder(pid, id, "new owner")
	return err
}

// refuseHolder names a holder of the port the session leaves alone, once
// per pid, so a stranger on the port is not reported every second.
func (s *session) refuseHolder(pid uint32, why string) {
	f := s.follow
	if !f.refused[pid] {
		f.refused[pid] = true
		_, _ = fmt.Fprintf(s.log, "iormesh: port %d: pid %d holds it but %s; not following\n", f.port, pid, why)
	}
}

// traceHolder traces pid, a holder of the port whose identity id was
// checked against the listing: into target_map with addRunning's
// liveness re-check, into the live set, a stderr line naming it as what,
// an -agent late attach (task lh4), and the correlator's attach event of
// this incarnation. It reports whether pid is traced now.
func (s *session) traceHolder(pid uint32, id correlate.Identity, what string) (bool, error) {
	added, err := s.addRunning(pid, id.StartNS)
	if err != nil || !added {
		return false, err
	}
	s.live[pid] = id.StartNS
	_, _ = fmt.Fprintf(s.log, "iormesh: port %d: following %s pid %d\n", s.follow.port, what, pid)
	s.late.exec(pid)
	return true, s.c.Seed(pid, id)
}

// awaitService makes a port session that starts with no target wait for
// the socket-activated service of act, the listener pinned at start
// (portActivation), saying so on stderr. A nil act — the port had
// owners, or the session is no port session — changes nothing.
func (s *session) awaitService(act *target.Activation) {
	if act == nil || s.follow == nil {
		return
	}
	s.follow.activation = act
	_, _ = fmt.Fprintf(s.log, "iormesh: port %d: only the service manager holds it; "+
		"waiting up to %v for the socket-activated service\n", s.follow.port, s.follow.grace)
}

// awaitActivation runs in followPort's place while a session that started
// on a port only the service manager held waits for the activated service
// (task lk4, decision #23). Each holder of the pinned socket that runs in
// a service unit of the manager (target.ProcFS.ActivatedHolders) and
// still has the listed start time is traced; any other holder is named
// once and left alone. Once one is traced, the adopted holders are
// remembered as start holders are (remember: the earliest-started one's
// executable), the pin is dropped, and later restarts or re-activations
// follow the ordinary rules. An unreadable listing is no error: the next
// resolve tries again.
func (s *session) awaitActivation() error {
	f := s.follow
	adopt, refuse, err := activatedHolders(*f.activation)
	if err != nil {
		return nil
	}
	for _, h := range refuse {
		s.refuseHolder(h.PID, "is not the socket-activated service (not in a service unit of the manager)")
	}
	var traced []uint32
	for _, h := range adopt {
		id := attachIdentity(h.PID)
		if id.StartNS == 0 || id.StartNS != h.StartNS {
			continue // gone already, or no longer the listed holder
		}
		ok, err := s.traceHolder(h.PID, id, "socket-activated service")
		if err != nil {
			return err
		}
		if ok {
			traced = append(traced, h.PID)
		}
	}
	if len(traced) > 0 {
		f.remember(traced)
		f.activation, f.since = nil, time.Time{}
	}
	return nil
}

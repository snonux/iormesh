package cli

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"iormesh/internal/target"
)

// fakeActivation installs activatedHolders, managerListener and
// resolvePort for a test: the holders of the pinned socket the session
// would adopt and refuse, each listed with its start time now
// (fakePort.start) unless fakePort.listed overrides it, whether pinning
// the manager's listener fails, and the owners a second resolve of the
// port finds (nil: it fails as the first did).
type fakeActivation struct {
	adopt, refuse []uint32
	pinErr        error
	resolved      []uint32
	resolves      int
}

func newFakeActivation(t *testing.T, f *fakePort) *fakeActivation {
	t.Helper()
	a := &fakeActivation{}
	origHolders, origListener, origResolve := activatedHolders, managerListener, resolvePort
	list := func(pids []uint32) []target.Holder {
		var hs []target.Holder
		for _, pid := range pids {
			start, ok := f.listed[pid]
			if !ok {
				start = f.start(pid)
			}
			hs = append(hs, target.Holder{PID: pid, StartNS: start})
		}
		return hs
	}
	activatedHolders = func(target.Activation) (adopt, refuse []target.Holder, err error) {
		return list(a.adopt), list(a.refuse), nil
	}
	managerListener = func(port uint16) (target.Activation, error) {
		return target.Activation{Port: port}, a.pinErr
	}
	resolvePort = func(target.Spec) ([]uint32, error) {
		a.resolves++
		if a.resolved == nil {
			return nil, fmt.Errorf("%w: %w", target.ErrSpec, target.ErrManagerOnly)
		}
		return a.resolved, nil
	}
	t.Cleanup(func() { activatedHolders, managerListener, resolvePort = origHolders, origListener, origResolve })
	return a
}

// activationSession is a -listen-port session on 8080 that started with
// only the service manager holding the port: no target, the listener
// pinned (portActivation).
func activationSession(t *testing.T, f *fakePort, grace time.Duration) (*session, *fakeTracer,
	*bytes.Buffer) {
	t.Helper()
	tr := newFakeTracer()
	c := testCorrelator(t, t.TempDir())
	t.Cleanup(func() { _ = c.Close() })
	s, log := portSession(t, f, c, tr, grace)
	cfg := runConfig{spec: target.Spec{ListenPort: 8080}, listenGrace: grace}
	pids, act, err := portActivation(cfg, target.ErrManagerOnly)
	if err != nil || act == nil || pids != nil {
		t.Fatalf("portActivation = %v, %v, %v; want the pinned listener", pids, act, err)
	}
	s.awaitService(act)
	if !strings.Contains(log.String(), "port 8080: only the service manager holds it; waiting up to") {
		t.Fatalf("stderr %q does not say the session waits", log.String())
	}
	return s, tr, log
}

// TestPortSessionAwaitsActivation (task lk4, decision #23): a session
// started on a port only systemd holds traces nothing until the
// activated service holds the pinned socket; then it adopts it (an
// attach, a stderr line), names a refused holder once, and from there on
// follows restarts by the ordinary rules, against the adopted service's
// owner key.
func TestPortSessionAwaitsActivation(t *testing.T) {
	f := newFakePort(t)
	a := newFakeActivation(t, f)
	s, tr, log := activationSession(t, f, time.Minute)
	mustEnded(t, s, false, "service idle, within the grace")

	app := target.Owner{Exe: "/usr/bin/app", UID: 995, Cgroup: "/system.slice/app.service"}
	f.now = f.now.Add(2 * time.Second)
	f.live[30], f.owners[30] = true, target.Owner{Exe: "/usr/bin/bash", UID: 1000}
	f.live[31], f.owners[31] = true, app
	a.refuse, a.adopt = []uint32{30}, []uint32{31}
	mustEnded(t, s, false, "service activated")
	if !slices.Equal(tr.added, []uint32{31}) || s.live[31] == 0 {
		t.Fatalf("added %v, live %v; want the activated service 31", tr.added, s.live)
	}
	f.now = f.now.Add(2 * time.Second)
	mustEnded(t, s, false, "service running")
	out := log.String()
	if !strings.Contains(out, "port 8080: following socket-activated service pid 31") ||
		strings.Count(out, "pid 30 holds") != 1 {
		t.Fatalf("stderr %q: want the adopted service named and the stranger named once", out)
	}

	// The service exits (idle timeout); systemd holds the port alone
	// again. Its re-activation is followed by the owner key, a stranger
	// binding the port is not.
	f.live[31], f.holders = false, nil
	a.adopt, a.refuse = nil, nil
	f.now = f.now.Add(2 * time.Second)
	mustEnded(t, s, false, "service gone, within the grace")
	f.now = f.now.Add(2 * time.Second)
	f.live[40], f.owners[40] = true, target.Owner{Exe: "/usr/bin/other", UID: 995}
	f.live[41], f.owners[41] = true, app
	f.holders = []uint32{40, 41}
	mustEnded(t, s, false, "service re-activated")
	if !slices.Equal(tr.added, []uint32{31, 41}) {
		t.Fatalf("added %v; want the re-activated service 41 after 31", tr.added)
	}
}

// TestPortSessionActivationTimeout (task lk4): no activation within the
// grace ends the session, stderr saying why; a holder refused meanwhile
// is never traced.
func TestPortSessionActivationTimeout(t *testing.T) {
	f := newFakePort(t)
	a := newFakeActivation(t, f)
	s, tr, log := activationSession(t, f, time.Minute)
	f.live[30] = true
	a.refuse = []uint32{30}
	mustEnded(t, s, false, "idle")
	f.now = f.now.Add(time.Minute)
	mustEnded(t, s, true, "no activation within the grace")
	if len(tr.added) != 0 {
		t.Fatalf("added %v; want nothing", tr.added)
	}
	if out := log.String(); !strings.Contains(out, "port 8080: no socket-activated service started within 1m0s") {
		t.Fatalf("stderr %q does not say why the session ends", out)
	}
}

// TestPortActivation (tasks lk4, el4): only a manager-only port with a
// grace waits for its service; -listen-grace 0, another resolve error
// and a listener that cannot be pinned keep the start's refusal — unless
// the pin failed because the service activated in between: the port is
// resolved once more and its owners are the session's start targets.
func TestPortActivation(t *testing.T) {
	f := newFakePort(t)
	a := newFakeActivation(t, f)
	cfg := runConfig{spec: target.Spec{ListenPort: 8080}, listenGrace: time.Minute}
	managerOnly := fmt.Errorf("%w: %w", target.ErrSpec, target.ErrManagerOnly)
	if pids, act, err := portActivation(cfg, managerOnly); err != nil || act == nil || act.Port != 8080 ||
		pids != nil {
		t.Fatalf("manager only: %v, %v, %v; want the pinned listener", pids, act, err)
	}
	other := fmt.Errorf("%w: nothing listens", target.ErrSpec)
	if pids, act, err := portActivation(cfg, other); !errors.Is(err, other) || act != nil || pids != nil {
		t.Fatalf("nothing listens: %v, %v, %v; want the error", pids, act, err)
	}
	noGrace := cfg
	noGrace.listenGrace = 0
	if pids, act, err := portActivation(noGrace, managerOnly); !errors.Is(err, target.ErrManagerOnly) ||
		act != nil || pids != nil {
		t.Fatalf("-listen-grace 0: %v, %v, %v; want the refusal", pids, act, err)
	}
	if a.resolves != 0 {
		t.Fatalf("resolved the port %d times without a failed pin; want none", a.resolves)
	}
	a.pinErr = errors.New("no cgroup")
	if pids, act, err := portActivation(cfg, managerOnly); !errors.Is(err, target.ErrManagerOnly) ||
		act != nil || pids != nil || a.resolves != 1 {
		t.Fatalf("unpinnable: %v, %v, %v after %d resolves; want the refusal after one",
			pids, act, err, a.resolves)
	}

	// The service activated between the start's resolve and the pin: pid
	// 31 holds the port now, and the second resolve names it.
	a.pinErr = fmt.Errorf("%w: pid 31 holds the listener on TCP port 8080: not socket activation", target.ErrSpec)
	a.resolved = []uint32{31}
	if pids, act, err := portActivation(cfg, managerOnly); err != nil || act != nil ||
		!slices.Equal(pids, []uint32{31}) || a.resolves != 2 {
		t.Fatalf("activated meanwhile: %v, %v, %v; want the owner 31", pids, act, err)
	}
}

// TestPortSessionActivationStartChanged (task el4): a holder of the
// pinned socket whose pid no longer has the start time the listing saw —
// it exited and the pid was reused — is not adopted; the next resolve,
// listing the right start time, adopts it.
func TestPortSessionActivationStartChanged(t *testing.T) {
	f := newFakePort(t)
	a := newFakeActivation(t, f)
	s, tr, _ := activationSession(t, f, time.Minute)
	f.now = f.now.Add(2 * time.Second)
	f.live[31], f.owners[31] = true, target.Owner{Exe: "/usr/bin/app", UID: 995}
	f.listed[31] = 7 // the listing saw another incarnation
	a.adopt = []uint32{31}
	mustEnded(t, s, false, "listed holder gone")
	if len(tr.added) != 0 || s.follow.activation == nil {
		t.Fatalf("added %v, pin %v; want nothing traced, still waiting", tr.added, s.follow.activation)
	}
	delete(f.listed, 31)
	f.now = f.now.Add(2 * time.Second)
	mustEnded(t, s, false, "service listed right")
	if !slices.Equal(tr.added, []uint32{31}) {
		t.Fatalf("added %v; want the service 31", tr.added)
	}
}

// TestPortSessionActivationOwnerUnreadable (task el4): an adopted
// service whose owner key cannot be read leaves the session nothing to
// recognise a restart by, so it keeps the pinned listener: once the
// service exits the session waits for the next activation instead of
// ending with a word about an executable "unreadable at start".
func TestPortSessionActivationOwnerUnreadable(t *testing.T) {
	f := newFakePort(t)
	a := newFakeActivation(t, f)
	s, tr, log := activationSession(t, f, time.Minute)
	f.now = f.now.Add(2 * time.Second)
	f.live[31] = true // no owner key
	a.adopt = []uint32{31}
	mustEnded(t, s, false, "service activated")
	if !slices.Equal(tr.added, []uint32{31}) {
		t.Fatalf("added %v; want the service 31", tr.added)
	}

	f.live[31], a.adopt = false, nil
	f.now = f.now.Add(2 * time.Second)
	mustEnded(t, s, false, "service gone, within the grace")
	f.live[32], f.owners[32] = true, target.Owner{Exe: "/usr/bin/app", UID: 995}
	a.adopt = []uint32{32}
	f.now = f.now.Add(2 * time.Second)
	mustEnded(t, s, false, "service re-activated")
	if !slices.Equal(tr.added, []uint32{31, 32}) {
		t.Fatalf("added %v; want the re-activated service 32 after 31", tr.added)
	}
	if out := log.String(); strings.Contains(out, "unreadable at start") {
		t.Fatalf("stderr %q blames the start", out)
	}
}

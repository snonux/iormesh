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

// fakeActivation installs activatedHolders and managerListener for a
// test: the holders of the pinned socket the session would adopt and
// refuse, each listed with its start time now (fakePort.start), and
// whether pinning the manager's listener fails.
type fakeActivation struct {
	adopt, refuse []uint32
	pinErr        error
}

func newFakeActivation(t *testing.T, f *fakePort) *fakeActivation {
	t.Helper()
	a := &fakeActivation{}
	origHolders, origListener := activatedHolders, managerListener
	list := func(pids []uint32) []target.Holder {
		var hs []target.Holder
		for _, pid := range pids {
			hs = append(hs, target.Holder{PID: pid, StartNS: f.start(pid)})
		}
		return hs
	}
	activatedHolders = func(target.Activation) (adopt, refuse []target.Holder, err error) {
		return list(a.adopt), list(a.refuse), nil
	}
	managerListener = func(port uint16) (target.Activation, error) {
		return target.Activation{Port: port}, a.pinErr
	}
	t.Cleanup(func() { activatedHolders, managerListener = origHolders, origListener })
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
	act, err := portActivation(cfg, target.ErrManagerOnly)
	if err != nil || act == nil {
		t.Fatalf("portActivation = %v, %v; want the pinned listener", act, err)
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

// TestPortActivation (task lk4): only a manager-only port with a grace
// waits for its service; -listen-grace 0, another resolve error and a
// listener that cannot be pinned keep the start's refusal.
func TestPortActivation(t *testing.T) {
	f := newFakePort(t)
	a := newFakeActivation(t, f)
	cfg := runConfig{spec: target.Spec{ListenPort: 8080}, listenGrace: time.Minute}
	managerOnly := fmt.Errorf("%w: %w", target.ErrSpec, target.ErrManagerOnly)
	if act, err := portActivation(cfg, managerOnly); err != nil || act == nil || act.Port != 8080 {
		t.Fatalf("manager only: %v, %v; want the pinned listener", act, err)
	}
	other := fmt.Errorf("%w: nothing listens", target.ErrSpec)
	if act, err := portActivation(cfg, other); !errors.Is(err, other) || act != nil {
		t.Fatalf("nothing listens: %v, %v; want the error", act, err)
	}
	noGrace := cfg
	noGrace.listenGrace = 0
	if act, err := portActivation(noGrace, managerOnly); !errors.Is(err, target.ErrManagerOnly) || act != nil {
		t.Fatalf("-listen-grace 0: %v, %v; want the refusal", act, err)
	}
	a.pinErr = errors.New("no cgroup")
	if act, err := portActivation(cfg, managerOnly); !errors.Is(err, target.ErrManagerOnly) || act != nil {
		t.Fatalf("unpinnable: %v, %v; want the refusal", act, err)
	}
}

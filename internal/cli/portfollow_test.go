package cli

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"iormesh/internal/correlate"
	"iormesh/internal/schema"
	"iormesh/internal/target"
)

// fakePort is the /proc a listening-port session sees in a test: which
// processes live, who holds the port and each process's owner key and
// start time.
type fakePort struct {
	live    map[uint32]bool
	holders []uint32 // nil: nothing listens
	owners  map[uint32]target.Owner
	// starts are the processes' start times now, 1 when not set; listed
	// overrides the start time the port's listing saw for a holder — a
	// pid reused between the listing and the adoption.
	starts, listed map[uint32]uint64
	now            time.Time
}

// start is pid's start time now (processIdentity).
func (f *fakePort) start(pid uint32) uint64 {
	if s, ok := f.starts[pid]; ok {
		return s
	}
	return 1
}

// newFakePort installs a fakePort for the test's processAlive,
// processIdentity, procCommand, listenOwners and procOwner.
func newFakePort(t *testing.T) *fakePort {
	t.Helper()
	fakeProcs(t)
	f := &fakePort{live: map[uint32]bool{}, owners: map[uint32]target.Owner{},
		starts: map[uint32]uint64{}, listed: map[uint32]uint64{}, now: time.Unix(1000, 0)}
	origOwners, origOwner := listenOwners, procOwner
	processAlive = func(pid uint32, start uint64) bool {
		return f.live[pid] && (start == 0 || start == f.start(pid))
	}
	processIdentity = func(pid uint32) correlate.Identity {
		return correlate.Identity{Comm: "x", StartNS: f.start(pid)}
	}
	listenOwners = func(uint16) ([]target.Holder, error) {
		if f.holders == nil {
			return nil, errors.New("nothing listens")
		}
		var hs []target.Holder
		for _, pid := range f.holders {
			start, ok := f.listed[pid]
			if !ok {
				start = f.start(pid)
			}
			hs = append(hs, target.Holder{PID: pid, StartNS: start})
		}
		return hs, nil
	}
	procOwner = func(pid uint32) (target.Owner, error) {
		o, ok := f.owners[pid]
		if !ok {
			return target.Owner{}, errors.New("no owner")
		}
		return o, nil
	}
	t.Cleanup(func() { listenOwners, procOwner = origOwners, origOwner })
	return f
}

// portSession is a started -listen-port session on port 8080 with the
// given grace, its clock the fakePort's, owning pids at start.
func portSession(t *testing.T, f *fakePort, c *correlate.Correlator, tr *fakeTracer,
	grace time.Duration, pids ...uint32) (*session, *bytes.Buffer) {
	t.Helper()
	cfg := runConfig{spec: target.Spec{ListenPort: 8080}, project: "p", duration: time.Hour, listenGrace: grace}
	s := newSession(tr, c, cfg)
	var log bytes.Buffer
	s.log = &log
	s.follow.now = func() time.Time { return f.now }
	if err := s.start(pids); err != nil {
		t.Fatal(err)
	}
	return s, &log
}

// mustEnded calls ended and fails the test on an error or the wrong
// verdict.
func mustEnded(t *testing.T, s *session, want bool, why string) {
	t.Helper()
	got, err := s.ended()
	if err != nil || got != want {
		t.Fatalf("%s: ended() = %v, %v; want %v", why, got, err, want)
	}
}

// TestPortSessionFollowsRestart (task mh4): the port's owner exits; the
// session waits instead of ending, ignores a holder that runs another
// program or as another user, adopts the restarted program under the
// same uid (an attach of its own incarnation, a stderr line), and ends
// once no new owner appears within the grace.
func TestPortSessionFollowsRestart(t *testing.T) {
	f := newFakePort(t)
	app := target.Owner{Exe: "/usr/bin/app", UID: 1000}
	f.live[10], f.owners[10], f.holders = true, app, []uint32{10}
	dir := t.TempDir()
	tr := newFakeTracer()
	c := testCorrelator(t, dir)
	s, log := portSession(t, f, c, tr, time.Minute, 10)
	mustEnded(t, s, false, "owner alive")

	f.live[10], f.holders = false, nil // the app exits and releases the port
	mustEnded(t, s, false, "owner gone, within the grace")

	f.now = f.now.Add(2 * time.Second)
	f.live[20], f.owners[20] = true, target.Owner{Exe: "/usr/bin/app", UID: 0}
	f.live[21], f.owners[21] = true, target.Owner{Exe: "/usr/bin/other", UID: 1000}
	f.live[22], f.owners[22] = true, app
	f.holders = []uint32{20, 21, 22}
	mustEnded(t, s, false, "restarted owner")
	if !slices.Equal(tr.added, []uint32{22}) || s.live[22] == 0 || len(s.live) != 1 {
		t.Fatalf("added %v, live %v; want only the restarted app 22", tr.added, s.live)
	}
	if !strings.Contains(log.String(), "port 8080: following new owner pid 22") {
		t.Fatalf("stderr %q does not name the new owner", log.String())
	}

	f.live[22], f.holders = false, nil
	f.now = f.now.Add(10 * time.Second)
	mustEnded(t, s, false, "second exit, within the grace")
	f.now = f.now.Add(time.Minute)
	mustEnded(t, s, true, "no new owner within the grace")
	if !strings.Contains(log.String(), "port 8080: no new owner within 1m0s") {
		t.Fatalf("stderr %q does not say why the session ends", log.String())
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	var attached []uint32
	for _, e := range readEvents(t, dir) {
		if e.Event == schema.KindAttach {
			attached = append(attached, e.PID)
		}
	}
	if !slices.Equal(attached, []uint32{10, 22}) {
		t.Fatalf("attach events of %v, want [10 22]", attached)
	}
}

// TestListenGraceFlag: -listen-grace defaults to defaultListenGrace,
// takes 0, and refuses a negative duration.
func TestListenGraceFlag(t *testing.T) {
	fakeProcs(t)
	base := []string{"-listen-port", "8080", "-project", "p", "-out", "o"}
	cfg, err := parseRunFlags(base, &bytes.Buffer{})
	if err != nil || cfg.listenGrace != defaultListenGrace {
		t.Fatalf("default grace %v, %v; want %v", cfg.listenGrace, err, defaultListenGrace)
	}
	cfg, err = parseRunFlags(append(base, "-listen-grace", "0"), &bytes.Buffer{})
	if err != nil || cfg.listenGrace != 0 {
		t.Fatalf("-listen-grace 0: %v, %v", cfg.listenGrace, err)
	}
	if _, err := parseRunFlags(append(base, "-listen-grace", "-1s"), &bytes.Buffer{}); !errors.Is(err, errUsage) {
		t.Fatalf("-listen-grace -1s: err %v, want a usage error", err)
	}
}

// TestPortSessionNoGrace:-listen-grace 0 keeps the old behaviour, the
// session ends as soon as its owner is gone, even if a matching process
// listens again at once.
func TestPortSessionNoGrace(t *testing.T) {
	f := newFakePort(t)
	app := target.Owner{Exe: "/usr/bin/app", UID: 1000}
	f.live[10], f.owners[10], f.holders = true, app, []uint32{10}
	tr := newFakeTracer()
	c := testCorrelator(t, t.TempDir())
	s, _ := portSession(t, f, c, tr, 0, 10)
	f.live[10] = false
	f.live[22], f.owners[22], f.holders = true, app, []uint32{22}
	mustEnded(t, s, true, "no grace")
	if len(tr.added) != 0 {
		t.Fatalf("added %v with no grace", tr.added)
	}
	_ = c.Close()
}

// TestPortSessionOwnerUnknown: an owner whose key could not be read at
// start leaves nothing to recognise a restart by, so the session ends
// when it exits, saying so, rather than adopting whatever binds the port.
func TestPortSessionOwnerUnknown(t *testing.T) {
	f := newFakePort(t)
	f.live[10], f.holders = true, []uint32{10}
	tr := newFakeTracer()
	c := testCorrelator(t, t.TempDir())
	s, log := portSession(t, f, c, tr, time.Minute, 10)
	f.live[10] = false
	f.live[22], f.owners[22], f.holders = true, target.Owner{Exe: "/usr/bin/app"}, []uint32{22}
	mustEnded(t, s, true, "unknown owner")
	if len(tr.added) != 0 || !strings.Contains(log.String(), "not following") {
		t.Fatalf("added %v, stderr %q; want nothing adopted and a reason", tr.added, log.String())
	}
	_ = c.Close()
}

// TestPortFollowPIDReuse (task qj4): the listing names pid 22 as the
// port's holder, but by the time the session adopts it pid 22 is another
// process — same executable and uid, a different start time — which does
// not hold the port. Adoption is tied to the incarnation the listing saw,
// so it is refused; the real holder is adopted at the next resolve.
func TestPortFollowPIDReuse(t *testing.T) {
	f := newFakePort(t)
	app := target.Owner{Exe: "/usr/bin/app", UID: 1000}
	f.live[10], f.owners[10], f.holders = true, app, []uint32{10}
	tr := newFakeTracer()
	c := testCorrelator(t, t.TempDir())
	s, _ := portSession(t, f, c, tr, time.Minute, 10)
	f.live[10], f.holders = false, nil
	mustEnded(t, s, false, "owner gone, within the grace")

	f.now = f.now.Add(2 * time.Second)
	f.live[22], f.owners[22], f.starts[22], f.listed[22] = true, app, 9, 5
	f.holders = []uint32{22}
	mustEnded(t, s, false, "listed holder reused")
	if len(tr.added) != 0 || len(s.live) != 0 {
		t.Fatalf("added %v, live %v; want the reused pid left alone", tr.added, s.live)
	}

	f.now = f.now.Add(2 * time.Second)
	delete(f.listed, 22) // the listing now sees the process at pid 22
	mustEnded(t, s, false, "real holder")
	if !slices.Equal(tr.added, []uint32{22}) {
		t.Fatalf("added %v; want 22 once the listing sees its incarnation", tr.added)
	}
	_ = c.Close()
}

// TestPortFollowPrivilegeSeparated (task qj4): a privilege-separated
// server — a root master and workers of the same binary under another
// uid, all holding the listener — is followed whole across a restart:
// the new master and its workers are adopted, since workers forked
// before the re-resolve are never picked up by fork-following.
func TestPortFollowPrivilegeSeparated(t *testing.T) {
	f := newFakePort(t)
	master := target.Owner{Exe: "/usr/sbin/nginx", UID: 0}
	worker := target.Owner{Exe: "/usr/sbin/nginx", UID: 995}
	f.live[10], f.owners[10], f.starts[10] = true, master, 100
	f.live[11], f.owners[11], f.starts[11] = true, worker, 200
	f.holders = []uint32{10, 11}
	tr := newFakeTracer()
	c := testCorrelator(t, t.TempDir())
	s, _ := portSession(t, f, c, tr, time.Minute, 10, 11)
	f.live[10], f.live[11], f.holders = false, false, nil
	mustEnded(t, s, false, "server gone, within the grace")

	f.now = f.now.Add(2 * time.Second)
	f.live[24], f.owners[24] = true, master
	f.live[25], f.owners[25] = true, worker
	f.holders = []uint32{24, 25}
	mustEnded(t, s, false, "restarted server")
	if !slices.Equal(tr.added, []uint32{24, 25}) {
		t.Fatalf("added %v; want the restarted master and worker [24 25]", tr.added)
	}
	_ = c.Close()
}

// TestPortFollowRemembersOriginalExe (task qj4): at start the port is
// held by the app (the earliest-started holder, uid 1000) and by a helper
// of another program, running as root, that inherited the listener fd.
// Only keys of the original holder's executable are remembered, so after
// the app exits the root helper binding the port is not adopted; the
// restarted app is.
func TestPortFollowRemembersOriginalExe(t *testing.T) {
	f := newFakePort(t)
	app := target.Owner{Exe: "/usr/bin/app", UID: 1000}
	helper := target.Owner{Exe: "/usr/bin/helper", UID: 0}
	f.live[10], f.owners[10], f.starts[10] = true, app, 100
	f.live[11], f.owners[11], f.starts[11] = true, helper, 200
	f.holders = []uint32{10, 11}
	tr := newFakeTracer()
	c := testCorrelator(t, t.TempDir())
	s, _ := portSession(t, f, c, tr, time.Minute, 10, 11)
	f.live[10], f.live[11], f.holders = false, false, nil
	mustEnded(t, s, false, "owners gone, within the grace")

	f.now = f.now.Add(2 * time.Second)
	f.live[23], f.owners[23] = true, helper
	f.holders = []uint32{23}
	mustEnded(t, s, false, "root helper on the port")
	if len(tr.added) != 0 {
		t.Fatalf("added %v; a holder of another program than the original widened the follow", tr.added)
	}

	f.now = f.now.Add(2 * time.Second)
	f.live[24], f.owners[24] = true, app
	f.holders = []uint32{23, 24}
	mustEnded(t, s, false, "restarted app")
	if !slices.Equal(tr.added, []uint32{24}) {
		t.Fatalf("added %v; want only the restarted app 24", tr.added)
	}
	_ = c.Close()
}

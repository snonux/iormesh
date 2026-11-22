package correlate

import (
	"testing"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// tickJoin is a Correlator feeding a joinTest's JavaJoin, both on the
// joinTest's clock: the live pipeline of `iormesh run`.
func tickJoin(t *testing.T) (*joinTest, *Correlator) {
	jt := newJoinTest(t)
	c := New(Config{Host: Host{ID: "h1", Project: "p"}, Sink: jt.j,
		ProcInfo: func(uint32) (string, uint64) { return "java", jvmStart },
		Clock:    func() uint64 { return jt.now }})
	return jt, c
}

// idle advances the clock in run-loop ticks until until, with no record
// arriving: the correlator's Tick, then the join's Poll, as session.poll
// does.
func idle(t *testing.T, jt *joinTest, c *Correlator, until uint64, tick bool) {
	t.Helper()
	for jt.now < until {
		jt.now += 200_000_000
		if tick {
			if err := c.Tick(); err != nil {
				t.Fatal(err)
			}
		}
		if err := jt.j.Poll(); err != nil {
			t.Fatal(err)
		}
	}
}

// TestIdleStreamHeldAcceptKeepsConn (task 1f4 review): a server's end of
// a connection whose client is on another host waits out the fuse window
// in the correlator, and on an idle stream no later record ends that
// wait. The run loop's Tick does: the end leaves after the window, ahead
// of the request's http record, which so finds its connection. Without
// the tick the end stays held and the http record, released by the
// join's clock timeout, has no conn_id — what this pins.
func TestIdleStreamHeldAcceptKeepsConn(t *testing.T) {
	for _, tick := range []bool{true, false} {
		jt, c := tickJoin(t)
		at := jt.now + 1000
		const local, remote = "10.0.0.2:8080", "10.0.0.1:40000"
		// Another process's syscall gives the join its envelope (project,
		// host), as the stream's first events always do.
		jt.unmarked(99, 99, "write", at-500)
		// The session takes the JVM on: its attach event makes its ring
		// readable (task yi4), though its socket event is still held.
		if err := c.Seed(jvmPID, Identity{Comm: "java", StartNS: jvmStart}); err != nil {
			t.Fatal(err)
		}
		if err := c.Handle(tcp6(at, 0xB, jvmPID, 7, types.TCP_STATE_SNAPSHOT, tcpEstablished,
			local, remote)); err != nil {
			t.Fatal(err)
		}
		jt.ring(jt.request(at+1000, "/", local, remote), jt.watermark(at+20_000_000))
		idle(t, jt, c, at+defaultFuseWindowNS+defaultTimeout+1_000_000_000, tick)
		state, http := connOf(jt.sink.events, schema.KindTCPState), connOf(jt.sink.events, schema.KindHTTP)
		switch {
		case tick && (http == nil || state == nil || http.ConnID == "" || http.ConnID != state.ConnID):
			t.Errorf("with Tick: events %v, want the http event on the tcp_state's connection",
				kinds(jt.sink.events))
		case !tick && (state != nil || http == nil || http.ConnID != ""):
			t.Errorf("without Tick: events %v, want the end still held and the http event "+
				"without a connection (else this test pins nothing)", kinds(jt.sink.events))
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// connOf is the first event of kind, nil if none.
func connOf(evs []schema.Event, kind string) *schema.Event {
	for i := range evs {
		if evs[i].Event == kind {
			return &evs[i]
		}
	}
	return nil
}

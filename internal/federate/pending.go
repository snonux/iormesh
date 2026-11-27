package federate

import (
	"math"
	"time"

	"iormesh/internal/schema"
	"iormesh/internal/segment"
)

// Pending halves (task cn4, decision #43). A running session publishes
// a segment a minute, on its own clock: each session's rotation falls
// at its start plus whole minutes, so sessions started seconds apart
// rotate seconds apart, and a collect pulls only published segments.
// The build therefore sees each running session up to a different
// instant, and a connection made in that tail — after the far end's
// last collected rotation, before the near end's — has only its near
// half in the store. Fuse calls it no_match, the service database
// counts it unresolved, and its edge reads mixed although both ends
// are traced (the M8 trial's three mixed edges, each the last round
// before the collect). A later collect brings the far half and fuses
// it, but every build has a new tail.
//
// So settle marks such a half pending instead: a no_match half is
// pending when the far end may still come, which takes both of
//
//   - the half lies in the build's last segment interval plus
//     settleSlack: at collect time C a running session's unpublished
//     records are all from its open segment, opened after C minus one
//     interval, and C is no earlier than the newest record of the
//     build (maxWall), so an older half's far end, if any session
//     traces it, is collected already; and
//   - a running session that did not record the half has collected
//     records only up to settleSlack after it (sessionSpan.last): its
//     open segment may hold the far end. A session that recorded its
//     end has published everything (the tracer's Close publishes the
//     last segment); a source that is no segment (an NDJSON trace
//     directory) is a finished run; and a session alone has no one to
//     bring the far end.
//
// A pending half is no fact about the far end yet, so it is left out of
// the unresolved counts and the service database (servicedb.Observe)
// until a build with newer segments fuses it or, once it is older than
// the window, calls it no_match.

// settleSlack widens both tests: an event stamped just before a
// rotation can be written into the next segment (window.go spanSlack),
// the writer rotates on the first tick after its interval, and the two
// ends of one connection are stamped by two hosts' clocks. Ten seconds
// covers these with room; a wider slack only keeps a truly one-sided
// half pending a little longer.
const settleSlack = 10 * time.Second

// sessionSpan is what the collect brought of one session that may still
// publish: its newest record and whether it recorded its end.
type sessionSpan struct {
	last  uint64 // wall ns
	ended bool
}

// sessionOf names the session of a source that may still publish
// segments the collect has not pulled — a collected Segment, whose
// Session names its tracer run — and "" for a finished one.
func sessionOf(s Source) string {
	if seg, ok := s.(interface{ Session() string }); ok {
		return seg.Session()
	}
	return ""
}

// span folds one event into maxWall and, when the source being read
// may still grow, into its session's span.
func (c *collector) span(ev *schema.Event) {
	c.maxWall = max(c.maxWall, ev.WallNS)
	if c.cur == "" {
		return
	}
	sp := c.spans[c.cur]
	if sp == nil {
		sp = &sessionSpan{}
		c.spans[c.cur] = sp
	}
	sp.last = max(sp.last, ev.WallNS)
	if ev.Event == schema.KindEnd && ev.Args.EndReason != nil {
		sp.ended = true
	}
}

// settle turns f's no_match halves whose far end may still come into
// pending ones (see above). Their order stays (host, conn_id).
func (c *collector) settle(f *Fusion) {
	for i := range f.Unresolved {
		if u := &f.Unresolved[i]; u.Reason == ReasonNoMatch && c.pending(u.Half) {
			u.Reason = ReasonPending
		}
	}
}

// pending reports whether h lies in the build's unsettled tail and some
// other running session's collected records end before h plus the
// slack.
func (c *collector) pending(h Half) bool {
	slack, interval := uint64(settleSlack), uint64(segment.DefaultInterval)
	t := h.Established
	if t > math.MaxUint64-slack-interval {
		return false // a forged stamp: never a tail
	}
	if t+slack+interval < c.maxWall {
		return false
	}
	own := map[string]bool{}
	for _, id := range append([]string{h.ConnID}, h.Aliases...) {
		if a := c.acc[LocalConn{h.Host, id}]; a != nil {
			for _, s := range a.sessions {
				own[s] = true
			}
		}
	}
	for name, sp := range c.spans {
		if !sp.ended && !own[name] && sp.last < t+slack {
			return true
		}
	}
	return false
}

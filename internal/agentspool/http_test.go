package agentspool

import (
	"reflect"
	"sync/atomic"
	"testing"
)

// httpBody is an HTTP body as Ring.http writes it: the fixed words, then
// the seven leading strings and the headers, zero-padded to 8.
func httpBody(h HTTP) []byte {
	strs := append([]string{h.Method, h.Scheme, h.Host, h.Path, h.Version, h.Local, h.Remote},
		h.ReqHeaders...)
	strs = append(strs, h.RespHeaders...)
	return httpRaw(h, uint32(len(strs)), uint32(len(h.ReqHeaders)), strs)
}

func httpRaw(h HTTP, n, nreq uint32, strs []string) []byte {
	body := le.AppendUint64(nil, h.ExitMono)
	body = le.AppendUint64(body, h.StackID)
	body = le.AppendUint64(body, h.ThreadID)
	body = le.AppendUint32(body, uint32(h.KTID))
	body = le.AppendUint32(body, uint32(h.Status))
	body = le.AppendUint32(body, n)
	body = le.AppendUint32(body, nreq)
	var flags uint32
	if h.Server {
		flags = httpServer
	}
	body = le.AppendUint32(body, flags)
	body = le.AppendUint32(body, h.Exchanges)
	body = appendOtel(body, h.Otel)
	for _, s := range strs {
		body = le.AppendUint32(body, uint32(len(s)))
		body = append(body, s...)
	}
	for len(body)%8 != 0 {
		body = append(body, 0)
	}
	return body
}

func sampleHTTP() HTTP {
	return HTTP{ExitMono: 99, StackID: 7, ThreadID: 3, KTID: 4242, Status: 404,
		Method: "GET", Scheme: "http", Host: "127.0.0.1", Path: "/x?token=***",
		Version: "HTTP/1.1", Local: "127.0.0.1:40000", Remote: "127.0.0.1:8080",
		ReqHeaders:  []string{"authorization: ***", "accept: */*"},
		RespHeaders: []string{"set-cookie: ***"}, Server: true, Exchanges: 2,
		Otel: Otel{TraceHi: 0xa3ce929d0e0e4736, TraceLo: 0x4bf92f3577b34d01, Span: 0x5e7e0000000000a1}}
}

// TestHTTPRoundTrip: an HTTP record decodes to what was written, headers
// split at the request-header count, and absent header lists stay nil.
func TestHTTPRoundTrip(t *testing.T) {
	path, w := newTestRing(t, testCap)
	full := sampleHTTP()
	bare := HTTP{ExitMono: 100, KTID: 1, Method: "POST", Scheme: "https", Path: "/", Exchanges: 1}
	w.raw(typeHTTP, httpBody(full))
	w.raw(typeHTTP, httpBody(bare))
	got := drainAll(t, openRing(t, path))
	if len(got) != 2 || !reflect.DeepEqual(got[0], full) || !reflect.DeepEqual(got[1], bare) {
		t.Fatalf("records %+v, want %+v and %+v", got, full, bare)
	}
}

// TestHTTPMalformed: bodies the agent never writes are skipped and
// counted, and the ring stays readable.
func TestHTTPMalformed(t *testing.T) {
	h := sampleHTTP()
	fixed := []string{"GET", "http", "", "/", "HTTP/1.1", "", ""}
	cases := map[string][]byte{
		"short":              make([]byte, 16),
		"too few strings":    httpRaw(h, 6, 0, fixed[:6]),
		"nreq past n":        httpRaw(h, 7, 1, fixed),
		"negative ktid":      httpBody(HTTP{KTID: -1, Method: "GET", Exchanges: 1}),
		"negative status":    httpBody(HTTP{Status: -1, Method: "GET", Exchanges: 1}),
		"bad utf8 path":      httpBody(HTTP{Path: "\xff", Method: "GET", Exchanges: 1}),
		"no exchange":        httpBody(HTTP{Method: "GET"}),
		"unknown flag":       setByte(httpBody(sampleHTTP()), 40, 2),
		"count past body":    httpRaw(h, 9, 0, fixed),
		"trailing garbage":   append(httpBody(h), 1, 0, 0, 0, 0, 0, 0, 0),
		"span without trace": httpBody(HTTP{Method: "GET", Exchanges: 1, Otel: Otel{Span: 1}}),
		"trace without span": httpBody(HTTP{Method: "GET", Exchanges: 1, Otel: Otel{TraceLo: 1}}),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path, w := newTestRing(t, testCap)
			w.raw(typeHTTP, body)
			w.bracket(Bracket{Hook: 5, ExitMono: 1})
			r := openRing(t, path)
			got := drainAll(t, r)
			if len(got) != 1 || got[0].(Bracket).Hook != 5 {
				t.Errorf("records %+v, want only the bracket after it", got)
			}
			if l := r.Losses(); l != (Losses{Malformed: 1}) {
				t.Errorf("losses %+v, want one malformed", l)
			}
		})
	}
}

// TestHTTPDropsCounted: the agent's HTTP drop counter surfaces as
// DroppedHTTP, part of Dropped and Total.
func TestHTTPDropsCounted(t *testing.T) {
	path, w := newTestRing(t, testCap)
	atomic.AddUint64(w.ptr(offDropped+8*typeHTTP), 3)
	l := openRing(t, path).Losses()
	if l.DroppedHTTP != 3 || l.Dropped() != 3 || l.DroppedOther != 0 {
		t.Errorf("losses %+v, want 3 dropped HTTP records", l)
	}
}

// TestOtelIDs: the W3C forms of the ids, zero-padded, and validity.
func TestOtelIDs(t *testing.T) {
	o := Otel{TraceHi: 0xa3ce929d0e0e4736, TraceLo: 0x0f, Span: 0xc11e0000000000a1}
	if o.TraceID() != "a3ce929d0e0e4736000000000000000f" || o.SpanID() != "c11e0000000000a1" {
		t.Errorf("ids %s %s", o.TraceID(), o.SpanID())
	}
	for _, c := range []struct {
		o    Otel
		want bool
	}{{o, true}, {Otel{}, false}, {Otel{Span: 1}, false}, {Otel{TraceLo: 1}, false},
		{Otel{TraceHi: 1, Span: 1}, true}} {
		if c.o.Valid() != c.want {
			t.Errorf("%+v valid = %v", c.o, !c.want)
		}
	}
}

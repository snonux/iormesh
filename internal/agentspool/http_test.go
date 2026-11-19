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
		RespHeaders: []string{"set-cookie: ***"}}
}

// TestHTTPRoundTrip: an HTTP record decodes to what was written, headers
// split at the request-header count, and absent header lists stay nil.
func TestHTTPRoundTrip(t *testing.T) {
	path, w := newTestRing(t, testCap)
	full := sampleHTTP()
	bare := HTTP{ExitMono: 100, KTID: 1, Method: "POST", Scheme: "https", Path: "/"}
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
		"short":            make([]byte, 16),
		"too few strings":  httpRaw(h, 6, 0, fixed[:6]),
		"nreq past n":      httpRaw(h, 7, 1, fixed),
		"negative ktid":    httpBody(HTTP{KTID: -1, Method: "GET"}),
		"negative status":  httpBody(HTTP{Status: -1, Method: "GET"}),
		"bad utf8 path":    httpBody(HTTP{Path: "\xff", Method: "GET"}),
		"count past body":  httpRaw(h, 9, 0, fixed),
		"trailing garbage": append(httpBody(h), 1, 0, 0, 0, 0, 0, 0, 0),
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

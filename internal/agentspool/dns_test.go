package agentspool

import (
	"reflect"
	"sync/atomic"
	"testing"
)

// dnsBody is a DNS body as Ring.dns writes it: the fixed words, then the
// host and its addresses, zero-padded to 8.
func dnsBody(d DNS, n uint32, strs []string) []byte {
	body := le.AppendUint64(nil, d.ExitMono)
	body = le.AppendUint64(body, d.StackID)
	body = le.AppendUint64(body, d.ThreadID)
	body = le.AppendUint32(body, uint32(d.KTID))
	body = le.AppendUint32(body, n)
	body = appendOtel(body, d.Otel)
	for _, s := range strs {
		body = le.AppendUint32(body, uint32(len(s)))
		body = append(body, s...)
	}
	for len(body)%8 != 0 {
		body = append(body, 0)
	}
	return body
}

func dnsOf(d DNS) []byte {
	strs := append([]string{d.Host}, d.Addrs...)
	return dnsBody(d, uint32(len(strs)), strs)
}

// TestDNSRoundTrip: a DNS record decodes to what was written.
func TestDNSRoundTrip(t *testing.T) {
	path, w := newTestRing(t, testCap)
	want := DNS{ExitMono: 9, StackID: 7, ThreadID: 3, KTID: 42, Host: "localhost",
		Addrs: []string{"127.0.0.1", "0:0:0:0:0:0:0:1"}, Otel: Otel{TraceLo: 1, Span: 2}}
	w.raw(typeDNS, dnsOf(want))
	got := drainAll(t, openRing(t, path))
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("records %+v, want %+v", got, want)
	}
}

// TestDNSMalformed: bodies the agent never writes are skipped and
// counted, and the ring stays readable.
func TestDNSMalformed(t *testing.T) {
	cases := map[string][]byte{
		"short":         make([]byte, 16),
		"no address":    dnsBody(DNS{}, 1, []string{"h"}),
		"empty host":    dnsOf(DNS{Addrs: []string{"10.0.0.1"}}),
		"negative ktid": dnsOf(DNS{KTID: -1, Host: "h", Addrs: []string{"10.0.0.1"}}),
		"half a span":   dnsOf(DNS{Host: "h", Addrs: []string{"a"}, Otel: Otel{Span: 1}}),
		"count past":    dnsBody(DNS{}, 4, []string{"h", "a"}),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path, w := newTestRing(t, testCap)
			w.raw(typeDNS, body)
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

// TestDNSDropsCounted: the agent's DNS drop counter surfaces as
// DroppedDNS, not as an HTTP loss.
func TestDNSDropsCounted(t *testing.T) {
	path, w := newTestRing(t, testCap)
	atomic.AddUint64(w.ptr(offDropped+8*typeDNS), 2)
	l := openRing(t, path).Losses()
	if l.DroppedDNS != 2 || l.DroppedHTTP != 0 || l.Dropped() != 2 || l.DroppedOther != 0 {
		t.Errorf("losses %+v, want 2 dropped DNS records", l)
	}
}

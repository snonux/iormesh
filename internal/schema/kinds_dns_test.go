package schema

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// dnsEvent is a valid dns event as the Java join emits it.
func dnsEvent() Event {
	e := validEvent()
	e.Event, e.FD, e.FDGen, e.Ret, e.Err, e.Args = KindDNS, NoFD, 0, 0, "", Args{}
	e.DNS = &DNS{Host: "localhost", Addrs: []string{"127.0.0.1", "0:0:0:0:0:0:0:1"}}
	return e
}

// TestDNSEventRoundTrip: a dns event decodes to itself and re-encodes to
// the same bytes, through AppendEvent too.
func TestDNSEventRoundTrip(t *testing.T) {
	want := dnsEvent()
	if err := want.Validate(); err != nil {
		t.Fatalf("valid dns event rejected: %v", err)
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeEvent(data)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip: %v\n got %+v\nwant %+v", err, got, want)
	}
	if enc := AppendEvent(nil, &want); string(enc) != string(data) {
		t.Fatalf("AppendEvent:\n%s\nwant\n%s", enc, data)
	}
}

// TestDNSEventInvariants: the dns object belongs to dns events alone and
// needs its host and an address.
func TestDNSEventInvariants(t *testing.T) {
	cases := map[string]func(*Event){
		"dns without object": func(e *Event) { e.DNS = nil },
		"dns without host":   func(e *Event) { e.DNS.Host = "" },
		"dns without addrs":  func(e *Event) { e.DNS.Addrs = nil },
		"object on a read":   func(e *Event) { e.Event = KindRead },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := dnsEvent()
			mutate(&e)
			if err := e.Validate(); !errors.Is(err, ErrInvalid) {
				t.Errorf("Validate() = %v, want ErrInvalid", err)
			}
		})
	}
}

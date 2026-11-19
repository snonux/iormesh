package schema

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// httpEvent is a valid http event as the Java join emits it: a client's
// request, joined to its connection and numbered on it.
func httpEvent() Event {
	e := validEvent()
	e.Event, e.FD, e.FDGen, e.Ret, e.Err, e.Args = KindHTTP, NoFD, 0, 0, "", Args{}
	e.HTTP = &HTTP{Method: "GET", Scheme: "http", Host: "127.0.0.1",
		Path: "/login?token=***", Status: 200, ReqSeq: 1, Version: "HTTP/1.1",
		ReqHeaders:  []string{"authorization: ***", "x-request-id: 7"},
		RespHeaders: []string{"set-cookie: ***"}}
	return e
}

// TestHTTPEventRoundTrip: an http event, headers and all, decodes to
// itself and re-encodes to the same bytes, through AppendEvent too.
func TestHTTPEventRoundTrip(t *testing.T) {
	want := httpEvent()
	if err := want.Validate(); err != nil {
		t.Fatalf("valid http event rejected: %v", err)
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

// TestHTTPEventInvariants: the http object belongs to http events alone,
// and an http event needs one with a method.
func TestHTTPEventInvariants(t *testing.T) {
	cases := map[string]func(*Event){
		"http without object": func(e *Event) { e.HTTP = nil },
		"http without method": func(e *Event) { e.HTTP.Method = "" },
		"object on a connect": func(e *Event) { e.Event = KindConnect },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := httpEvent()
			mutate(&e)
			if err := e.Validate(); !errors.Is(err, ErrInvalid) {
				t.Errorf("Validate() = %v, want ErrInvalid", err)
			}
		})
	}
}

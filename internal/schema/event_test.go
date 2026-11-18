package schema

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// openatLine is the guide's M1 example line (docs/implementation.md, "The
// event envelope"); the test keeps that example honest.
const openatLine = `{"schema_version":1,"project":"demo","seq":412,` +
	`"boot_ns":91822003417,"wall_ns":1794953012000112233,"host_id":"3f2a",` +
	`"pid":4711,"pid_incarnation":91700000000,"tid":4719,"comm":"java",` +
	`"event":"openat","fd":7,"fd_gen":1,"ret":7,"latency_ns":18211,` +
	`"bytes":0,"args":{"path":"/etc/orders.conf","flags":0},` +
	`"attribution":"none"}`

func ptr[T any](v T) *T { return &v }

// validEvent returns a fully populated event that passes Validate; tests
// break one invariant at a time from here.
func validEvent() Event {
	return Event{
		SchemaVersion: Version, Project: "demo", Seq: 1, BootNS: 10,
		WallNS: 20, HostID: "3f2a", PID: 4711, PIDInc: 91700000000,
		TID: 4719, Comm: "java", Event: KindConnect, FD: 6, FDGen: 2,
		Ret: -115, Err: "EINPROGRESS", LatencyNS: 5, Bytes: 0,
		Args: Args{
			RemoteAddr: ptr("::ffff:127.0.0.1"), RemotePort: ptr(uint16(8080)),
			LocalAddr: ptr("::ffff:127.0.0.1"), LocalPort: ptr(uint16(40000)),
		},
		ConnID:      "0123456789abcdef",
		Attribution: AttributionJava,
		Java:        &Java{Thread: "main", Stack: []uint64{3, 1}},
		HTTP:        &HTTP{Method: "GET", Scheme: "http", Host: "h", Path: "/", Status: 200, ReqSeq: 1},
	}
}

func TestEventRoundTrip(t *testing.T) {
	cases := map[string]Event{
		"full": validEvent(),
		"marker": func() Event {
			e := validEvent()
			e.Event, e.FD, e.FDGen, e.Ret, e.Err = KindMarker, NoFD, 0, 0, ""
			e.Args = Args{Marker: ptr("file-basic/begin")}
			e.Attribution, e.Java, e.HTTP, e.ConnID = AttributionNone, nil, nil, ""
			return e
		}(),
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			got, err := DecodeEvent(data)
			if err != nil {
				t.Fatalf("DecodeEvent(%s): %v", data, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("round trip changed the event:\n got %+v\nwant %+v", got, want)
			}
			again, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if string(again) != string(data) {
				t.Fatalf("re-encoding differs:\n%s\n%s", again, data)
			}
		})
	}
}

// TestGuideExampleLine decodes the documented line and re-encodes it to
// the same bytes: key order is struct order, and the example must stay
// what M1 actually emits.
func TestGuideExampleLine(t *testing.T) {
	ev, err := DecodeEvent([]byte(openatLine))
	if err != nil {
		t.Fatal(err)
	}
	if *ev.Args.Path != "/etc/orders.conf" || *ev.Args.Flags != 0 || ev.FDGen != 1 {
		t.Fatalf("decoded wrong values: %+v", ev)
	}
	out, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != openatLine {
		t.Fatalf("re-encoded line differs:\n got %s\nwant %s", out, openatLine)
	}
}

// TestDecodeEventRejectsMalformed covers inputs that are not a valid
// envelope at the JSON level: they must fail before any consumer sees them.
func TestDecodeEventRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"not json":        `{"schema_version":`,
		"not an object":   `[1,2]`,
		"null":            `null`,
		"unknown key":     strings.Replace(openatLine, `"comm"`, `"bogus":1,"comm"`, 1),
		"unknown arg key": strings.Replace(openatLine, `"flags":0`, `"flags":0,"mode":420`, 1),
		"trailing data":   openatLine + ` {}`,
		"negative pid":    strings.Replace(openatLine, `"pid":4711`, `"pid":-1`, 1),
		"string seq":      strings.Replace(openatLine, `"seq":412`, `"seq":"412"`, 1),
		"port overflow":   strings.Replace(openatLine, `"flags":0`, `"flags":0,"local_port":70000`, 1),
	}
	for _, key := range eventRequiredKeys {
		cases["missing "+key] = dropKey(t, openatLine, key)
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeEvent([]byte(line)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("DecodeEvent(%s) = %v, want ErrInvalid", line, err)
			}
		})
	}
}

// TestEventValidateRejects breaks one semantic invariant per case.
func TestEventValidateRejects(t *testing.T) {
	cases := map[string]func(*Event){
		"schema_version 0":         func(e *Event) { e.SchemaVersion = 0 },
		"schema_version 2":         func(e *Event) { e.SchemaVersion = Version + 1 },
		"empty project":            func(e *Event) { e.Project = "" },
		"empty host_id":            func(e *Event) { e.HostID = "" },
		"seq 0":                    func(e *Event) { e.Seq = 0 },
		"unknown kind":             func(e *Event) { e.Event = "frobnicate" },
		"empty kind":               func(e *Event) { e.Event = "" },
		"fd below NoFD":            func(e *Event) { e.FD = -2 },
		"fd_gen without fd":        func(e *Event) { e.FD = NoFD },
		"err with ret >= 0":        func(e *Event) { e.Ret = 0 },
		"empty attribution":        func(e *Event) { e.Attribution = "" },
		"bogus attribution":        func(e *Event) { e.Attribution = "jvm" },
		"java attribution no java": func(e *Event) { e.Java = nil },
		"none attribution w/ java": func(e *Event) { e.Attribution = AttributionNone },
		"openat without path":      func(e *Event) { e.Event = KindOpenat },
		"marker without name":      func(e *Event) { e.Event = KindMarker },
		"marker empty name":        func(e *Event) { e.Event, e.Args.Marker = KindMarker, ptr("") },
		"tcp_state without state":  func(e *Event) { e.Event = KindTCPState },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			e := validEvent()
			breakIt(&e)
			if err := e.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() = %v, want ErrInvalid", err)
			}
			data, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeEvent(data); !errors.Is(err, ErrInvalid) {
				t.Fatalf("DecodeEvent accepted an invalid event: %v", err)
			}
		})
	}
}

func TestEventValidateAcceptsEveryKind(t *testing.T) {
	kinds := []string{KindOpen, KindOpenat, KindOpenat2, KindRead, KindWrite,
		KindClose, KindConnect, KindAccept, KindAccept4, KindTCPState,
		KindMarker, KindDrop, KindSession, KindEnd}
	for _, k := range kinds {
		e := validEvent()
		e.Event = k
		e.Args.Path = ptr("/x")
		e.Args.Marker = ptr("s/begin")
		e.Args.SockState = ptr("TCP_ESTABLISHED")
		if err := e.Validate(); err != nil {
			t.Errorf("kind %s: %v", k, err)
		}
	}
}

// TestEventRequiredKeysMatchTags keeps eventRequiredKeys in step with the
// struct: every field without omitempty is required, nothing else is.
func TestEventRequiredKeysMatchTags(t *testing.T) {
	var want []string
	rt := reflect.TypeFor[Event]()
	for i := range rt.NumField() {
		name, opts, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		if !strings.Contains(opts, "omitempty") {
			want = append(want, name)
		}
	}
	if !slices.Equal(eventRequiredKeys, want) {
		t.Fatalf("eventRequiredKeys = %v, struct tags say %v", eventRequiredKeys, want)
	}
}

// dropKey removes one top-level key from a JSON object.
func dropKey(t *testing.T, line, key string) string {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		t.Fatal(err)
	}
	if _, ok := obj[key]; !ok {
		t.Fatalf("key %q not in %s", key, line)
	}
	delete(obj, key)
	out, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

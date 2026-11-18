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

// validSession is a complete session header payload.
func validSession() *Session {
	return &Session{BootID: "b1d2", FQDN: "host.example.org", Kernel: "6.12.0", Arch: "x86_64"}
}

// hostEvent returns a valid host-level (pid 0) event of kind: the shape
// the correlator gives session, drop and end events.
func hostEvent(kind string) Event {
	e := Event{SchemaVersion: Version, Project: "demo", Seq: 1, BootNS: 10, WallNS: 20,
		HostID: "3f2a", Event: kind, FD: NoFD, Attribution: AttributionNone}
	switch kind {
	case KindSession:
		e.Session = validSession()
	case KindDrop:
		e.Args.Lost = ptr(uint64(3))
	case KindEnd:
		e.Args.EndReason = ptr(EndTargetsExited)
	}
	return e
}

func TestEventRoundTrip(t *testing.T) {
	cases := map[string]Event{
		"full":    validEvent(),
		"session": hostEvent(KindSession),
		"drop":    hostEvent(KindDrop),
		"end":     hostEvent(KindEnd),
		"exec": func() Event {
			e := validEvent()
			e.Event, e.FD, e.FDGen, e.Ret, e.Err = KindExec, NoFD, 0, 0, ""
			e.Args = Args{Path: ptr("/usr/bin/java"),
				Argv: []string{"java", "-Dpassword=***", "Main"}}
			e.Attribution, e.Java, e.HTTP, e.ConnID = AttributionNone, nil, nil, ""
			return e
		}(),
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
		// encoding/json alone folds case: "Comm" would overwrite "comm".
		"case-folded duplicate": strings.Replace(openatLine, `"comm":"java"`, `"comm":"java","Comm":"evil"`, 1),
		"case-folded key":       strings.Replace(openatLine, `"comm"`, `"COMM"`, 1),
		"case-folded arg key":   strings.Replace(openatLine, `"path"`, `"Path"`, 1),
		"case-folded java key": strings.Replace(openatLine, `"attribution":"none"`,
			`"attribution":"java","java":{"Stack":[1]}`, 1),
		"case-folded http key": strings.Replace(openatLine, `"attribution":"none"`,
			`"attribution":"none","http":{"METHOD":"GET"}`, 1),
		"unknown java key": strings.Replace(openatLine, `"attribution":"none"`,
			`"attribution":"java","java":{"stack":[1],"frames":[]}`, 1),
	}
	for _, key := range requiredKeys(reflect.TypeFor[Event]()) {
		cases["missing "+key] = dropKey(t, openatLine, key)
		cases["null "+key] = setKey(t, openatLine, key, "null")
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
		"schema_version 0":          func(e *Event) { e.SchemaVersion = 0 },
		"schema_version 2":          func(e *Event) { e.SchemaVersion = Version + 1 },
		"empty project":             func(e *Event) { e.Project = "" },
		"empty host_id":             func(e *Event) { e.HostID = "" },
		"seq 0":                     func(e *Event) { e.Seq = 0 },
		"unknown kind":              func(e *Event) { e.Event = "frobnicate" },
		"empty kind":                func(e *Event) { e.Event = "" },
		"fd below NoFD":             func(e *Event) { e.FD = -2 },
		"fd_gen without fd":         func(e *Event) { e.FD = NoFD },
		"err with ret >= 0":         func(e *Event) { e.Ret = 0 },
		"empty attribution":         func(e *Event) { e.Attribution = "" },
		"bogus attribution":         func(e *Event) { e.Attribution = "jvm" },
		"java attribution no java":  func(e *Event) { e.Java = nil },
		"java attribution no stack": func(e *Event) { e.Java.Stack = nil },
		"openat empty path":         func(e *Event) { e.Event, e.Args.Path = KindOpenat, ptr("") },
		"none attribution w/ java":  func(e *Event) { e.Attribution = AttributionNone },
		"openat without path":       func(e *Event) { e.Event = KindOpenat },
		"marker without name":       func(e *Event) { e.Event = KindMarker },
		"marker empty name":         func(e *Event) { e.Event, e.Args.Marker = KindMarker, ptr("") },
		"tcp_state without state":   func(e *Event) { e.Event = KindTCPState },
		"argv on a non-exec event":  func(e *Event) { e.Args.Argv = []string{"java"} },
		// M2 step 7's args stay with their kinds.
		"signal without args.signal": func(e *Event) { e.Event = KindSignal },
		"signal without a receiver": func(e *Event) {
			e.Event, e.PID, e.Args.Signal = KindSignal, 0, ptr(int32(15))
		},
		"args.signal on a connect":     func(e *Event) { e.Args.Signal = ptr(int32(15)) },
		"args.target_pid on a connect": func(e *Event) { e.Args.TargetPID = ptr(uint32(1)) },
		"args.sender_pid on a kill": func(e *Event) {
			e.Event, e.Args.SenderPID = KindKill, ptr(uint32(1))
		},
		"args.sender_pid on a connect": func(e *Event) { e.Args.SenderPID = ptr(uint32(1)) },
		"fd2_gen without fd2": func(e *Event) {
			e.Event, e.Args.FD2Gen = KindPipe2, ptr(uint32(1))
		},
		"fd2 on a connect": func(e *Event) { e.Args.FD2 = ptr(int32(7)) },
		// M2 step 11: a count names the calls of a folded read or write.
		"args.count on a connect": func(e *Event) { e.Args.Count = ptr(uint64(2)) },
		"args.count 1 on a read": func(e *Event) {
			e.Event, e.Err, e.Ret, e.Args.Count = KindRead, "", 1, ptr(uint64(1))
		},
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			e := validEvent()
			breakIt(&e)
			checkRejected(t, e)
		})
	}
}

// TestHostEventValidateRejects breaks the session, drop and end
// invariants one at a time.
func TestHostEventValidateRejects(t *testing.T) {
	cases := map[string]struct {
		kind    string
		breakIt func(*Event)
	}{
		"drop without lost":       {KindDrop, func(e *Event) { e.Args.Lost = nil }},
		"drop with zero lost":     {KindDrop, func(e *Event) { e.Args.Lost = ptr(uint64(0)) }},
		"end without reason":      {KindEnd, func(e *Event) { e.Args.EndReason = nil }},
		"end with unknown reason": {KindEnd, func(e *Event) { e.Args.EndReason = ptr("signal") }},
		"end with empty reason":   {KindEnd, func(e *Event) { e.Args.EndReason = ptr("") }},
		"session without object":  {KindSession, func(e *Event) { e.Session = nil }},
		"session empty boot_id":   {KindSession, func(e *Event) { e.Session.BootID = "" }},
		"session empty fqdn":      {KindSession, func(e *Event) { e.Session.FQDN = "" }},
		"session empty kernel":    {KindSession, func(e *Event) { e.Session.Kernel = "" }},
		"session empty arch":      {KindSession, func(e *Event) { e.Session.Arch = "" }},
		"session object on end":   {KindEnd, func(e *Event) { e.Session = validSession() }},
		"lost on end":             {KindEnd, func(e *Event) { e.Args.Lost = ptr(uint64(1)) }},
		"end_reason on drop":      {KindDrop, func(e *Event) { e.Args.EndReason = ptr(EndStop) }},
		"lost on session":         {KindSession, func(e *Event) { e.Args.Lost = ptr(uint64(1)) }},
		"unknown drop reason": {KindDrop, func(e *Event) {
			e.PID, e.Args.DropReason = 7, ptr("ring_buffer")
		}},
		"attach_gap without pid": {KindDrop, func(e *Event) { e.Args.DropReason = ptr(DropAttachGap) }},
		"drop_reason on end":     {KindEnd, func(e *Event) { e.Args.DropReason = ptr(DropAttachGap) }},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := hostEvent(c.kind)
			c.breakIt(&e)
			checkRejected(t, e)
		})
	}
}

// TestAttachGapDropValid: a drop that names its process and the
// attach_gap reason is a valid event and survives a round trip.
func TestAttachGapDropValid(t *testing.T) {
	e := hostEvent(KindDrop)
	e.PID, e.PIDInc, e.TID, e.Args.DropReason = 4711, 99, 4711, ptr(DropAttachGap)
	if err := e.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeEvent(data)
	if err != nil || got.Args.DropReason == nil || *got.Args.DropReason != DropAttachGap {
		t.Fatalf("round trip: %+v, %v", got.Args, err)
	}
}

// TestSessionKeysStrict: the session object is decoded as strictly as the
// envelope — a missing or unknown key fails.
func TestSessionKeysStrict(t *testing.T) {
	data, err := json.Marshal(hostEvent(KindSession))
	if err != nil {
		t.Fatal(err)
	}
	line := string(data)
	for name, bad := range map[string]string{
		"missing kernel": strings.Replace(line, `"kernel":"6.12.0",`, ``, 1),
		"unknown key":    strings.Replace(line, `"arch":`, `"cpus":4,"arch":`, 1),
		"case-folded":    strings.Replace(line, `"fqdn"`, `"FQDN"`, 1),
	} {
		if bad == line {
			t.Fatalf("%s: edit did not apply to %s", name, line)
		}
		if _, err := DecodeEvent([]byte(bad)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: DecodeEvent = %v, want ErrInvalid", name, err)
		}
	}
}

// checkRejected asserts both Validate and the strict decoder reject e.
func checkRejected(t *testing.T, e Event) {
	t.Helper()
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
}

func TestEventValidateAcceptsEveryKind(t *testing.T) {
	kinds := []string{KindOpen, KindOpenat, KindOpenat2, KindRead, KindWrite,
		KindClose, KindConnect, KindAccept, KindAccept4, KindTCPState,
		KindMarker, KindDrop, KindSession, KindEnd, KindExec}
	kinds = append(kinds, stepSevenKinds()...)
	for _, k := range kinds {
		e := validEvent()
		e.Event = k
		e.Args.Path = ptr("/x")
		e.Args.Marker = ptr("s/begin")
		e.Args.SockState = ptr("TCP_ESTABLISHED")
		switch k {
		case KindDrop:
			e.Args.Lost = ptr(uint64(1))
		case KindEnd:
			e.Args.EndReason = ptr(EndStop)
		case KindSession:
			e.Session = validSession()
		}
		withStepSevenArgs(&e)
		if err := e.Validate(); err != nil {
			t.Errorf("kind %s: %v", k, err)
		}
	}
}

// stepSevenKinds are the 22 kinds of M2 step 7 (kinds_ipc.go).
func stepSevenKinds() []string {
	return []string{KindBind, KindSendto, KindRecvfrom, KindPipe, KindPipe2,
		KindSocketpair, KindKill, KindTgkill, KindTkill, KindSignal,
		KindEpollCreate, KindEpollCreate1, KindEpollCtl, KindEpollWait,
		KindEpollPwait, KindEpollPwait2, KindPoll, KindPpoll,
		KindSendfile64, KindSplice, KindTee, KindCopyFileRange}
}

// withStepSevenArgs sets the step 7 args the event's kind may carry, so
// the accept test also proves each kind takes its own args.
func withStepSevenArgs(e *Event) {
	if fd2Kind(e.Event) {
		e.Args.FD2, e.Args.FD2Gen = ptr(int32(7)), ptr(uint32(1))
	}
	if signalKind(e.Event) {
		e.Args.Signal = ptr(int32(15))
		if e.Event == KindSignal {
			e.Args.SenderPID = ptr(uint32(1))
		} else {
			e.Args.TargetPID = ptr(uint32(2))
		}
	}
}

// TestStepSevenKindsAreKnown: every step 7 kind is a schema kind and the
// list above is complete — 22, as kinds_ipc.go declares.
func TestStepSevenKindsAreKnown(t *testing.T) {
	kinds := stepSevenKinds()
	if len(kinds) != 22 {
		t.Fatalf("%d step 7 kinds listed, want 22", len(kinds))
	}
	for _, k := range kinds {
		if !KnownKind(k) || !ipcKind(k) {
			t.Errorf("%s is not a known step 7 kind", k)
		}
	}
}

// TestRequiredKeysFollowTags pins the required set of the envelope, so a
// tag edit that silently makes a field optional (or required) shows up.
func TestRequiredKeysFollowTags(t *testing.T) {
	want := []string{"args", "attribution", "boot_ns", "bytes", "comm",
		"event", "fd", "fd_gen", "host_id", "latency_ns", "pid",
		"pid_incarnation", "project", "ret", "schema_version", "seq", "tid",
		"wall_ns"}
	if got := requiredKeys(reflect.TypeFor[Event]()); !slices.Equal(got, want) {
		t.Fatalf("required keys = %v, want %v", got, want)
	}
}

// requiredKeys lists t's required json keys, sorted.
func requiredKeys(t reflect.Type) []string {
	var keys []string
	for name, f := range jsonFields(t) {
		if f.required {
			keys = append(keys, name)
		}
	}
	slices.Sort(keys)
	return keys
}

// dropKey removes one top-level key from a JSON object.
func dropKey(t *testing.T, line, key string) string {
	t.Helper()
	return editKey(t, line, key, func(obj map[string]json.RawMessage) { delete(obj, key) })
}

// setKey replaces one top-level key's raw JSON value.
func setKey(t *testing.T, line, key, raw string) string {
	t.Helper()
	return editKey(t, line, key, func(obj map[string]json.RawMessage) {
		obj[key] = json.RawMessage(raw)
	})
}

func editKey(t *testing.T, line, key string, edit func(map[string]json.RawMessage)) string {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		t.Fatal(err)
	}
	if _, ok := obj[key]; !ok {
		t.Fatalf("key %q not in %s", key, line)
	}
	edit(obj)
	out, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestFoldedReadWriteValid: a folded read or write — args.count ≥ 2 — is a
// valid event (M2 step 11).
func TestFoldedReadWriteValid(t *testing.T) {
	for _, kind := range []string{KindRead, KindWrite} {
		e := validEvent()
		e.Event, e.Err, e.Ret, e.Bytes, e.Args = kind, "", 4000, 4000, Args{Count: ptr(uint64(1000))}
		if err := e.Validate(); err != nil {
			t.Errorf("folded %s: %v", kind, err)
		}
	}
}

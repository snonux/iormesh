package normalize

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"iormesh/internal/schema"
)

// symbolRE matches one expectation symbol: $p1 (process), $t1 (thread),
// $fd1 (fd instance), $port1, $h1 (host), $c1 (connection), $a1 (app —
// the node every incarnation of a pattern-matched app links to, M2; only
// edges name it, binding the whole node id). "$tmp" is not a symbol — the
// normalizer writes it literally into paths. Go's regexp prefers the
// earlier alternative, so "$port1" is a port, not $p.
var symbolRE = regexp.MustCompile(`\$(fd|port|p|t|h|c|a)[1-9][0-9]*`)

// Expectation is a scenario's declared golden (workload/scenarios/<name>/
// expect.json): the events and edges the trace must contain, and the
// strings it must never contain. See docs/implementation.md
// "Expectations (the golden format)".
type Expectation struct {
	Scenario      string          `json:"scenario"`
	SchemaVersion uint32          `json:"schema_version"`
	Events        []ExpectedEvent `json:"events"`
	Edges         []ExpectedEdge  `json:"edges"`
	MustNotAppear []string        `json:"must_not_appear"`
}

// ExpectedEvent is one declared event. Unset fields are not compared:
// a declaration says what must hold, not everything the event carries.
type ExpectedEvent struct {
	// Process binds the event's process ($p1); multi-process scenarios
	// set it so events and edges name the same process.
	Process string       `json:"process,omitempty"`
	Thread  string       `json:"thread"`
	Event   string       `json:"event"`
	FD      *Ref         `json:"fd,omitempty"`
	Bytes   *uint64      `json:"bytes,omitempty"`
	ConnID  *Ref         `json:"conn_id,omitempty"`
	Args    ExpectedArgs `json:"args"`
	// Attribution, when set, must equal the event's ("java" or "none"):
	// how a golden says a process is not a JVM (non-java-peer), so a
	// tracer that guessed Java frames for it fails the check.
	Attribution string `json:"attribution,omitempty"`
	// Java, when set, must match the event's Java attribution (M3, task
	// 2b4); see ExpectedJava. It implies attribution java.
	Java *ExpectedJava `json:"java,omitempty"`
	// HTTP, when set, must match an http event's request (M3, task 3b4);
	// see ExpectedHTTP. Only http events declare it.
	HTTP *ExpectedHTTP `json:"http,omitempty"`
	// Otel is the OpenTelemetry span the event ran in (M3 step 8, task
	// 4b4); it requires attribution java. Like args.count it is compared
	// when absent too: a declaration without it matches only an event
	// outside any span, so a golden says where the spans end as well —
	// an agent that tagged the server's request reads, read before the
	// handler made its span current, would fail it.
	Otel *ExpectedOtel `json:"otel,omitempty"`
}

// ExpectedOtel declares an event's span: both ids, W3C form (32 and 16
// lowercase hex digits), as the event's otel object carries them.
type ExpectedOtel struct {
	TraceID string `json:"trace_id"`
	SpanID  string `json:"span_id"`
}

// ExpectedHTTP declares an http event's request. Unset fields are not
// compared; a declared field must equal the event's, the path after
// redaction ("/login?user=***"). Each declared header line must be among
// the event's ("authorization: ***"), which may carry more: a golden says
// which headers prove the redaction, not every header the JDK adds.
type ExpectedHTTP struct {
	Method      string   `json:"method,omitempty"`
	Scheme      string   `json:"scheme,omitempty"`
	Host        string   `json:"host,omitempty"`
	Path        string   `json:"path,omitempty"`
	Status      *uint16  `json:"status,omitempty"`
	ReqSeq      *uint32  `json:"req_seq,omitempty"`
	Version     string   `json:"version,omitempty"`
	ReqHeaders  []string `json:"req_headers,omitempty"`
	RespHeaders []string `json:"resp_headers,omitempty"`
}

// ExpectedJava declares who caused an event on the Java side. Thread,
// when set, is the Java thread's name. Method is the causing method as
// "class#method" (binary class name, '$' for nested classes): the
// innermost frame of the event's stack outside the JDK's own packages
// (CausingMethod), its (file:line) dropped — a golden names the
// scenario's method, not the JDK internals it called or the line, which
// moves with every edit of the workload.
type ExpectedJava struct {
	Thread string `json:"thread,omitempty"`
	Method string `json:"method"`
}

// ExpectedArgs mirrors schema.Args. Paths compare after normalization
// ("$tmp/…"), addresses after IPv4-mapped unmapping. Flags compare by
// access mode plus the declared bits (see flagsMatch).
type ExpectedArgs struct {
	Path       *string `json:"path,omitempty"`
	NewPath    *string `json:"new_path,omitempty"`
	Flags      *uint32 `json:"flags,omitempty"`
	LocalAddr  *string `json:"local_addr,omitempty"`
	LocalPort  *Ref    `json:"local_port,omitempty"`
	RemoteAddr *string `json:"remote_addr,omitempty"`
	RemotePort *Ref    `json:"remote_port,omitempty"`
	SockState  *string `json:"sock_state,omitempty"`
	Marker     *string `json:"marker,omitempty"`
	// FD2 binds the event's second fd instance (a pipe's other end, M2
	// step 7) like FD binds the first; Signal is the signal number.
	FD2    *Ref   `json:"fd2,omitempty"`
	Signal *int32 `json:"signal,omitempty"`
	// Count is a folded read/write's args.count (M2 step 11). Unlike the
	// other fields it is compared when absent too: a declaration without
	// it means one call and matches only a single-call event, never a
	// folded loop whose summed bytes happen to equal the declared ones.
	Count *uint64 `json:"count,omitempty"`
}

// ExpectedEdge is one declared graph edge. From and To are node-id
// templates: a whole symbol ("$p1") or a symbol inside an id
// ("file:$h1:$tmp/a.dat", "conn:$c1"). An empty State matches any.
type ExpectedEdge struct {
	Type  string `json:"type"`
	From  string `json:"from"`
	To    string `json:"to"`
	State string `json:"state,omitempty"`
}

// Ref is a value that is either a symbol, bound on first match, or a
// literal (a well-known port, fd 0–2), written as a JSON number.
type Ref struct{ v string }

// LoadExpectation reads and validates an expect.json file.
func LoadExpectation(path string) (Expectation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Expectation{}, fmt.Errorf("normalize: %w", err)
	}
	x, err := ParseExpectation(data)
	if err != nil {
		return Expectation{}, fmt.Errorf("%s: %w", path, err)
	}
	return x, nil
}

// ParseExpectation decodes an expectation strictly — unknown keys and
// trailing data are errors, since a misspelled key would silently drop a
// declared assertion — and validates it.
func ParseExpectation(data []byte) (Expectation, error) {
	var x Expectation
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&x); err != nil {
		return Expectation{}, fmt.Errorf("normalize: expectation: %w", err)
	}
	if dec.More() {
		return Expectation{}, fmt.Errorf("normalize: expectation: trailing data")
	}
	if err := x.Validate(); err != nil {
		return Expectation{}, err
	}
	return x, nil
}

// UnmarshalJSON accepts a symbol string or a non-negative integer.
func (r *Ref) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		if s == "" || symbolRE.FindString(s) != s {
			return fmt.Errorf("normalize: %q is not a symbol", s)
		}
		r.v = s
		return nil
	}
	var n uint64
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("normalize: ref %s is neither a symbol nor a number", data)
	}
	r.v = strconv.FormatUint(n, 10)
	return nil
}

// String returns the symbol or the literal.
func (r Ref) String() string { return r.v }

// symbol reports whether r is a symbol rather than a literal.
func (r Ref) symbol() bool { return r.v != "" && r.v[0] == '$' }

// symbolKind is the kind letters of a symbol: "$port1" → "port".
func symbolKind(sym string) string {
	m := symbolRE.FindStringSubmatch(sym)
	if m == nil {
		return ""
	}
	return m[1]
}

// Validate checks the expectation's own consistency: version, known event
// kinds, symbols of the right kind in each place, and no thread declared
// in two processes.
func (x Expectation) Validate() error {
	if x.SchemaVersion != schema.Version {
		return fmt.Errorf("normalize: expectation schema_version %d, want %d",
			x.SchemaVersion, schema.Version)
	}
	if x.Scenario == "" {
		return fmt.Errorf("normalize: expectation without scenario")
	}
	threadProc := map[string]string{}
	for i, e := range x.Events {
		if err := e.validate(threadProc); err != nil {
			return fmt.Errorf("normalize: expectation event %d: %w", i+1, err)
		}
	}
	for i, e := range x.Edges {
		if e.Type == "" || e.From == "" || e.To == "" {
			return fmt.Errorf("normalize: expectation edge %d: type, from and to are required", i+1)
		}
	}
	for _, s := range x.MustNotAppear {
		if s == "" {
			return fmt.Errorf("normalize: empty must_not_appear entry matches everything")
		}
	}
	return nil
}

// validate checks one declared event; threadProc remembers each thread's
// process across events.
func (e ExpectedEvent) validate(threadProc map[string]string) error {
	if !schema.KnownKind(e.Event) {
		return fmt.Errorf("unknown event %q", e.Event)
	}
	switch e.Attribution {
	case "", schema.AttributionJava, schema.AttributionNone:
	default:
		return fmt.Errorf("unknown attribution %q", e.Attribution)
	}
	if err := e.validateJava(); err != nil {
		return err
	}
	if e.HTTP != nil && e.Event != schema.KindHTTP {
		return fmt.Errorf("http declared on a %s event", e.Event)
	}
	if err := e.validateOtel(); err != nil {
		return err
	}
	if err := wantKind(e.Thread, "t", false); err != nil {
		return fmt.Errorf("thread: %w", err)
	}
	if err := wantKind(e.Process, "p", true); err != nil {
		return fmt.Errorf("process: %w", err)
	}
	if p, ok := threadProc[e.Thread]; ok && p != e.Process {
		return fmt.Errorf("thread %s declared in processes %q and %q", e.Thread, p, e.Process)
	}
	threadProc[e.Thread] = e.Process
	refs := []struct {
		r    *Ref
		kind string
	}{{e.FD, "fd"}, {e.ConnID, "c"}, {e.Args.LocalPort, "port"}, {e.Args.RemotePort, "port"},
		{e.Args.FD2, "fd"}}
	for _, ref := range refs {
		if ref.r != nil && ref.r.symbol() && symbolKind(ref.r.v) != ref.kind {
			return fmt.Errorf("%s is not a $%s symbol", ref.r.v, ref.kind)
		}
	}
	return nil
}

// wantKind requires s to be one symbol of kind (empty allowed if
// optional).
func wantKind(s, kind string, optional bool) error {
	if s == "" && optional {
		return nil
	}
	if symbolRE.FindString(s) != s || symbolKind(s) != kind {
		return fmt.Errorf("%q is not a $%s symbol", s, kind)
	}
	return nil
}

// validateJava requires a java declaration to name its method as
// "class#method" and not to contradict its attribution.
func (e ExpectedEvent) validateJava() error {
	if e.Java == nil {
		return nil
	}
	if e.Attribution == schema.AttributionNone {
		return fmt.Errorf("java declared with attribution none")
	}
	cls, method, ok := strings.Cut(e.Java.Method, "#")
	if !ok || cls == "" || method == "" || strings.ContainsAny(method, "#()") {
		return fmt.Errorf("java method %q: want class#method", e.Java.Method)
	}
	return nil
}

// otelIDRE: W3C trace-context ids are lowercase hex — 32 digits for a
// trace id, 16 for a span id (all-zero ids are invalid, checked apart).
var (
	traceIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)
	spanIDRE  = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// validateOtel requires a declared span to come with attribution java —
// the ids are the agent's — and to name valid W3C ids, so a typo cannot
// declare a span no event could carry.
func (e ExpectedEvent) validateOtel() error {
	o := e.Otel
	switch {
	case o == nil:
		return nil
	case e.Attribution != schema.AttributionJava:
		return fmt.Errorf("otel declared without attribution java")
	case !traceIDRE.MatchString(o.TraceID) || strings.Trim(o.TraceID, "0") == "":
		return fmt.Errorf("otel.trace_id %q is not a valid W3C trace id", o.TraceID)
	case !spanIDRE.MatchString(o.SpanID) || strings.Trim(o.SpanID, "0") == "":
		return fmt.Errorf("otel.span_id %q is not a valid W3C span id", o.SpanID)
	}
	return nil
}

package normalize

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"

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
	}{{e.FD, "fd"}, {e.ConnID, "c"}, {e.Args.LocalPort, "port"}, {e.Args.RemotePort, "port"}}
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

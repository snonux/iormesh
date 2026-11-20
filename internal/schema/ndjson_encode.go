package schema

import "unicode/utf8"

// AppendEvent appends ev as one JSON object (no newline) to dst and returns
// the extended slice. Its output is byte-identical to json.Marshal(ev):
// keys in struct order, omitempty honoured, strings escaped the way
// encoding/json escapes them (HTML-safe, \ufffd for invalid UTF-8, U+2028
// and U+2029 escaped). TestAppendEventMatchesMarshal holds it to that on
// randomly filled events, every field of the schema types included, so a
// field added to Event without teaching the encoder fails the test.
//
// It exists for decision #25's per-event CPU budget: json.Marshal's
// reflection walk was over half the tracer's userspace time per event on
// syscall-burst (task wc4, docs/spikes/s3-overhead.md "Follow-up"). The
// NDJSON reader stays encoding/json, strict, so the format is still
// defined by the struct tags, not by this function. Integers go through
// appendUint (ndjson_uint.go), strconv's output at about twice its speed
// (task ud4).
func AppendEvent(dst []byte, ev *Event) []byte {
	dst = append(dst, `{"schema_version":`...)
	dst = appendUint(dst, uint64(ev.SchemaVersion))
	dst = appendStringField(dst, `,"project":`, ev.Project)
	dst = appendUintField(dst, `,"seq":`, ev.Seq)
	dst = appendUintField(dst, `,"boot_ns":`, ev.BootNS)
	dst = appendUintField(dst, `,"wall_ns":`, ev.WallNS)
	dst = appendStringField(dst, `,"host_id":`, ev.HostID)
	dst = appendUintField(dst, `,"pid":`, uint64(ev.PID))
	dst = appendUintField(dst, `,"pid_incarnation":`, ev.PIDInc)
	dst = appendUintField(dst, `,"tid":`, uint64(ev.TID))
	dst = appendStringField(dst, `,"comm":`, ev.Comm)
	dst = appendStringField(dst, `,"event":`, ev.Event)
	dst = appendIntField(dst, `,"fd":`, int64(ev.FD))
	dst = appendUintField(dst, `,"fd_gen":`, uint64(ev.FDGen))
	dst = appendIntField(dst, `,"ret":`, ev.Ret)
	if ev.Err != "" {
		dst = appendStringField(dst, `,"err":`, ev.Err)
	}
	dst = appendUintField(dst, `,"latency_ns":`, ev.LatencyNS)
	dst = appendUintField(dst, `,"bytes":`, ev.Bytes)
	dst = append(dst, `,"args":`...)
	dst = appendArgs(dst, &ev.Args)
	if ev.ConnID != "" {
		dst = appendStringField(dst, `,"conn_id":`, ev.ConnID)
	}
	dst = appendStringField(dst, `,"attribution":`, ev.Attribution)
	return appendEventTail(dst, ev)
}

// appendEventTail appends the optional nested objects, the optional
// stack_id (M2 step 9), otel (M3 step 8) and dns (task wd4), in struct
// order, then closes the event's object.
func appendEventTail(dst []byte, ev *Event) []byte {
	if ev.Java != nil {
		dst = append(dst, `,"java":`...)
		dst = appendJava(dst, ev.Java)
	}
	if ev.HTTP != nil {
		dst = append(dst, `,"http":`...)
		dst = appendHTTP(dst, ev.HTTP)
	}
	if ev.Session != nil {
		s := ev.Session
		dst = appendStringField(dst, `,"session":{"boot_id":`, s.BootID)
		dst = appendStringField(dst, `,"fqdn":`, s.FQDN)
		dst = appendStringField(dst, `,"kernel":`, s.Kernel)
		dst = appendStringField(dst, `,"arch":`, s.Arch)
		dst = append(dst, '}')
	}
	if ev.StackID != nil {
		dst = appendUintField(dst, `,"stack_id":`, uint64(*ev.StackID))
	}
	if ev.Otel != nil {
		dst = appendStringField(dst, `,"otel":{"trace_id":`, ev.Otel.TraceID)
		dst = appendStringField(dst, `,"span_id":`, ev.Otel.SpanID)
		dst = append(dst, '}')
	}
	if ev.DNS != nil {
		dst = appendStringField(dst, `,"dns":{"host":`, ev.DNS.Host)
		dst = append(dst, `,"addrs":`...)
		if ev.DNS.Addrs == nil {
			dst = append(dst, "null"...) // as json.Marshal; Validate rejects it
		} else {
			dst = appendStringArray(dst, ev.DNS.Addrs)
		}
		dst = append(dst, '}')
	}
	return append(dst, '}')
}

// object appends the members of one JSON object whose members are all
// optional: it writes the separating comma only between members, which
// the caller cannot know in advance.
type object struct {
	dst   []byte
	empty bool
}

func openObject(dst []byte) object {
	return object{dst: append(dst, '{'), empty: true}
}

// key appends a member's key (with its leading comma when needed).
func (o *object) key(name string) {
	if !o.empty {
		o.dst = append(o.dst, ',')
	}
	o.empty = false
	o.dst = append(o.dst, '"')
	o.dst = append(o.dst, name...)
	o.dst = append(o.dst, '"', ':')
}

func (o *object) str(name string, v *string) {
	if v != nil {
		o.key(name)
		o.dst = appendJSONString(o.dst, *v)
	}
}

func (o *object) close() []byte {
	return append(o.dst, '}')
}

// optUint and optInt append an optional numeric member: present exactly
// when the pointer is set, as omitempty does for pointers.
func optUint[T uint16 | uint32 | uint64](o *object, name string, v *T) {
	if v != nil {
		o.key(name)
		o.dst = appendUint(o.dst, uint64(*v))
	}
}

func optInt[T int32 | int64](o *object, name string, v *T) {
	if v != nil {
		o.key(name)
		o.dst = appendInt(o.dst, int64(*v))
	}
}

// appendArgs appends Args in struct order; every member is optional, so
// an event without arguments gets "{}", as json.Marshal writes it.
func appendArgs(dst []byte, a *Args) []byte {
	o := openObject(dst)
	o.str("path", a.Path)
	o.str("new_path", a.NewPath)
	optUint(&o, "flags", a.Flags)
	optUint(&o, "cmd", a.Cmd)
	optInt(&o, "offset", a.Offset)
	o.str("local_addr", a.LocalAddr)
	optUint(&o, "local_port", a.LocalPort)
	o.str("remote_addr", a.RemoteAddr)
	optUint(&o, "remote_port", a.RemotePort)
	o.str("sock_state", a.SockState)
	o.str("marker", a.Marker)
	optUint(&o, "lost", a.Lost)
	o.str("drop_reason", a.DropReason)
	o.str("end_reason", a.EndReason)
	if len(a.Argv) > 0 {
		o.key("argv")
		o.dst = appendStringArray(o.dst, a.Argv)
	}
	optInt(&o, "fd2", a.FD2)
	optUint(&o, "fd2_gen", a.FD2Gen)
	optInt(&o, "signal", a.Signal)
	optUint(&o, "target_pid", a.TargetPID)
	optUint(&o, "sender_pid", a.SenderPID)
	optUint(&o, "count", a.Count)
	optUint(&o, "snd_una", a.SndUna)
	optUint(&o, "rcv_nxt", a.RcvNxt)
	optUint(&o, "bytes_acked", a.BytesAcked)
	optUint(&o, "bytes_received", a.BytesReceived)
	optUint(&o, "tcp_seq", a.TCPSeq)
	o.str("pattern", a.Pattern)
	optUint(&o, "parent_pid", a.ParentPID)
	optUint(&o, "parent_incarnation", a.ParentInc)
	return o.close()
}

// appendJava appends the Java attribution; every member is omitempty.
func appendJava(dst []byte, j *Java) []byte {
	o := openObject(dst)
	if j.Thread != "" {
		o.str("thread", &j.Thread)
	}
	if len(j.Stack) > 0 {
		o.key("stack")
		o.dst = append(o.dst, '[')
		for i, id := range j.Stack {
			if i > 0 {
				o.dst = append(o.dst, ',')
			}
			o.dst = appendUint(o.dst, id)
		}
		o.dst = append(o.dst, ']')
	}
	if j.Carrier {
		o.key("carrier")
		o.dst = append(o.dst, "true"...)
	}
	return o.close()
}

// appendHTTP appends the HTTP object; every member is
// omitempty, so zero values are left out, not only nil ones.
func appendHTTP(dst []byte, h *HTTP) []byte {
	o := openObject(dst)
	for _, m := range [...]struct{ name, v string }{
		{"method", h.Method}, {"scheme", h.Scheme}, {"host", h.Host}, {"path", h.Path},
	} {
		if m.v != "" {
			o.key(m.name)
			o.dst = appendJSONString(o.dst, m.v)
		}
	}
	if h.Status != 0 {
		optUint(&o, "status", &h.Status)
	}
	if h.ReqSeq != 0 {
		optUint(&o, "req_seq", &h.ReqSeq)
	}
	if h.Version != "" {
		o.key("version")
		o.dst = appendJSONString(o.dst, h.Version)
	}
	for _, l := range [...]struct {
		name string
		v    []string
	}{{"req_headers", h.ReqHeaders}, {"resp_headers", h.RespHeaders}} {
		if len(l.v) > 0 {
			o.key(l.name)
			o.dst = appendStringArray(o.dst, l.v)
		}
	}
	optUint(&o, "req_bytes", h.ReqBytes)
	optUint(&o, "resp_bytes", h.RespBytes)
	if h.Held {
		o.key("held")
		o.dst = append(o.dst, "true"...)
	}
	return o.close()
}

func appendStringArray(dst []byte, ss []string) []byte {
	dst = append(dst, '[')
	for i, s := range ss {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = appendJSONString(dst, s)
	}
	return append(dst, ']')
}

// appendStringField, appendUintField and appendIntField append a
// required member; key carries its leading comma and colon.
func appendStringField(dst []byte, key, v string) []byte {
	return appendJSONString(append(dst, key...), v)
}

func appendUintField(dst []byte, key string, v uint64) []byte {
	return appendUint(append(dst, key...), v)
}

func appendIntField(dst []byte, key string, v int64) []byte {
	return appendInt(append(dst, key...), v)
}

const hexDigits = "0123456789abcdef"

// appendJSONString quotes s as encoding/json does with HTML escaping on
// (json.Marshal's default): ", \ and control bytes escaped (\b \f \n \r
// \t by name, the rest as \u00XX), <, > and & as \u003c, \u003e and
// \u0026, each byte of invalid UTF-8 as \ufffd, and U+2028/U+2029 as
// \u2028 and \u2029. Plain ASCII, the common case, is copied in runs.
func appendJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := safePrefix(s); i < len(s); {
		b := s[i]
		if b < utf8.RuneSelf {
			if jsonSafe(b) {
				i++
				continue
			}
			dst = appendEscapedByte(append(dst, s[start:i]...), b)
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			dst = append(append(dst, s[start:i]...), `\ufffd`...)
		case r == '\u2028' || r == '\u2029':
			dst = append(append(dst, s[start:i]...), '\\', 'u', '2', '0', '2', hexDigits[r&0xF])
		default:
			i += size
			continue
		}
		i += size
		start = i
	}
	return append(append(dst, s[start:]...), '"')
}

// Byte-lane masks of safeWord.
const (
	lanes01 = 0x0101010101010101
	lanes80 = 0x8080808080808080
)

// safePrefix returns how many leading bytes of s are, by whole 8-byte
// words, plain ASCII that jsonSafe copies as is: appendJSONString skips
// them eight at a time instead of byte by byte (task ud4; the event's
// strings — host id, project, comm, kind, attribution, thread — are
// nearly always all safe).
func safePrefix(s string) int {
	i := 0
	for ; i+8 <= len(s); i += 8 {
		w := uint64(s[i]) | uint64(s[i+1])<<8 | uint64(s[i+2])<<16 | uint64(s[i+3])<<24 |
			uint64(s[i+4])<<32 | uint64(s[i+5])<<40 | uint64(s[i+6])<<48 | uint64(s[i+7])<<56
		if !safeWord(w) {
			break
		}
	}
	return i
}

// safeWord reports whether none of w's eight bytes needs escaping: none
// has its top bit set (non-ASCII, decoded rune by rune), is a control
// byte (< 0x20), or is one of " \ < > &. Each test is the classic
// "has a zero byte" trick, (x − 0x01…) & ^x & 0x80…, whose borrow can
// only start at a byte that matches, so a word without one tests 0.
func safeWord(w uint64) bool {
	zero := func(x uint64) uint64 { return (x - lanes01) & ^x & lanes80 }
	less := (w - lanes01*0x20) & ^w & lanes80 // a byte < 0x20
	bad := w&lanes80 | less |
		zero(w^(lanes01*'"')) | zero(w^(lanes01*'\\')) |
		zero(w^(lanes01*'<')) | zero(w^(lanes01*'>')) | zero(w^(lanes01*'&'))
	return bad == 0
}

// jsonSafe reports whether an ASCII byte is copied as is.
func jsonSafe(b byte) bool {
	return jsonSafeASCII[b]
}

// jsonSafeASCII is jsonSafe as a table: one load per byte instead of six
// compares, on the loop every string of every event runs through.
var jsonSafeASCII = func() (t [utf8.RuneSelf]bool) {
	for b := range t {
		t[b] = b >= 0x20 && b != '"' && b != '\\' && b != '<' && b != '>' && b != '&'
	}
	return t
}()

// appendEscapedByte appends the escape of an ASCII byte jsonSafe rejects.
func appendEscapedByte(dst []byte, b byte) []byte {
	switch b {
	case '\\', '"':
		return append(dst, '\\', b)
	case '\b':
		return append(dst, '\\', 'b')
	case '\f':
		return append(dst, '\\', 'f')
	case '\n':
		return append(dst, '\\', 'n')
	case '\r':
		return append(dst, '\\', 'r')
	case '\t':
		return append(dst, '\\', 't')
	}
	return append(dst, '\\', 'u', '0', '0', hexDigits[b>>4], hexDigits[b&0xF])
}

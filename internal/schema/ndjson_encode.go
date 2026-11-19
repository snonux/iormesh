package schema

import (
	"strconv"
	"unicode/utf8"
)

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
// defined by the struct tags, not by this function.
func AppendEvent(dst []byte, ev *Event) []byte {
	dst = append(dst, `{"schema_version":`...)
	dst = strconv.AppendUint(dst, uint64(ev.SchemaVersion), 10)
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

// appendEventTail appends the optional nested objects and the optional
// stack_id (M2 step 9), then closes the event's object.
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
		o.dst = strconv.AppendUint(o.dst, uint64(*v), 10)
	}
}

func optInt[T int32 | int64](o *object, name string, v *T) {
	if v != nil {
		o.key(name)
		o.dst = strconv.AppendInt(o.dst, int64(*v), 10)
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
			o.dst = strconv.AppendUint(o.dst, id, 10)
		}
		o.dst = append(o.dst, ']')
	}
	if j.Carrier {
		o.key("carrier")
		o.dst = append(o.dst, "true"...)
	}
	return o.close()
}

// appendHTTP appends the provisional HTTP object; every member is
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
	return strconv.AppendUint(append(dst, key...), v, 10)
}

func appendIntField(dst []byte, key string, v int64) []byte {
	return strconv.AppendInt(append(dst, key...), v, 10)
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
	for i := 0; i < len(s); {
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

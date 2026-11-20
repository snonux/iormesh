package segment

import (
	"fmt"
	"slices"
	"strings"

	parquetgo "github.com/parquet-go/parquet-go"

	"iormesh/internal/schema"
)

// The events file's leaf columns, in the order of the parquet schema
// parquet-go derives from schema.Event (task ee4). eventColumns.add
// writes an event into them by hand instead of by the library's
// reflection, which was the bulk of the segment writer's per-event cost
// (decision #25's tracer-CPU item). newEventColumns checks this list
// against the derived schema — path, physical type and levels — so a
// field appended to schema.Event without a column here fails every
// writer's start (and the tests) instead of writing a wrong file.
const (
	colSchemaVersion = iota
	colProject
	colSeq
	colBootNS
	colWallNS
	colHostID
	colPID
	colPIDInc
	colTID
	colComm
	colEvent
	colFD
	colFDGen
	colRet
	colErr
	colLatencyNS
	colBytes
	colArgsPath
	colArgsNewPath
	colArgsFlags
	colArgsCmd
	colArgsOffset
	colArgsLocalAddr
	colArgsLocalPort
	colArgsRemoteAddr
	colArgsRemotePort
	colArgsSockState
	colArgsMarker
	colArgsLost
	colArgsDropReason
	colArgsEndReason
	colArgsArgv
	colArgsFD2
	colArgsFD2Gen
	colArgsSignal
	colArgsTargetPID
	colArgsSenderPID
	colArgsCount
	colArgsSndUna
	colArgsRcvNxt
	colArgsBytesAcked
	colArgsBytesReceived
	colArgsTCPSeq
	colArgsPattern
	colArgsParentPID
	colArgsParentInc
	colConnID
	colAttribution
	colJavaThread
	colJavaStack
	colJavaCarrier
	colHTTPMethod
	colHTTPScheme
	colHTTPHost
	colHTTPPath
	colHTTPStatus
	colHTTPReqSeq
	colHTTPVersion
	colHTTPReqHeaders
	colHTTPRespHeaders
	colHTTPReqBytes
	colHTTPRespBytes
	colSessionBootID
	colSessionFQDN
	colSessionKernel
	colSessionArch
	colStackID
	colOtelTraceID
	colOtelSpanID
	colDNSHost
	colDNSAddrs
	numEventColumns
)

// columnSpec names a column by its dotted schema path and says how it is
// buffered and encoded.
//
// seq, boot_ns, wall_ns and latency_ns are DELTA_BINARY_PACKED: on a
// syscall stream they are the high-entropy columns, which zstd spent
// ~100 ns per row each on as PLAIN int64 (task ee4, measured on a real
// -spool burst), while their deltas pack into a few bits — seq's into
// none.
type columnSpec struct {
	path string
	kind colKind
}

var eventColumnSpecs = [numEventColumns]columnSpec{
	colSchemaVersion: {"schema_version", kindInt32}, colProject: {"project", kindDict},
	colSeq: {"seq", kindDelta64}, colBootNS: {"boot_ns", kindDelta64}, colWallNS: {"wall_ns", kindDelta64},
	colHostID: {"host_id", kindDict}, colPID: {"pid", kindInt32},
	colPIDInc: {"pid_incarnation", kindInt64}, colTID: {"tid", kindInt32},
	colComm: {"comm", kindDict}, colEvent: {"event", kindDict}, colFD: {"fd", kindInt32},
	colFDGen: {"fd_gen", kindInt32}, colRet: {"ret", kindInt64}, colErr: {"err", kindDict},
	colLatencyNS: {"latency_ns", kindDelta64}, colBytes: {"bytes", kindInt64},
	colArgsPath: {"args.path", kindBytes}, colArgsNewPath: {"args.new_path", kindBytes},
	colArgsFlags: {"args.flags", kindInt32}, colArgsCmd: {"args.cmd", kindInt32},
	colArgsOffset: {"args.offset", kindInt64}, colArgsLocalAddr: {"args.local_addr", kindBytes},
	colArgsLocalPort: {"args.local_port", kindInt32}, colArgsRemoteAddr: {"args.remote_addr", kindBytes},
	colArgsRemotePort: {"args.remote_port", kindInt32}, colArgsSockState: {"args.sock_state", kindBytes},
	colArgsMarker: {"args.marker", kindBytes}, colArgsLost: {"args.lost", kindInt64},
	colArgsDropReason: {"args.drop_reason", kindBytes}, colArgsEndReason: {"args.end_reason", kindBytes},
	colArgsArgv: {"args.argv.list.element", kindBytes}, colArgsFD2: {"args.fd2", kindInt32},
	colArgsFD2Gen: {"args.fd2_gen", kindInt32}, colArgsSignal: {"args.signal", kindInt32},
	colArgsTargetPID: {"args.target_pid", kindInt32}, colArgsSenderPID: {"args.sender_pid", kindInt32},
	colArgsCount: {"args.count", kindInt64}, colArgsSndUna: {"args.snd_una", kindInt32},
	colArgsRcvNxt: {"args.rcv_nxt", kindInt32}, colArgsBytesAcked: {"args.bytes_acked", kindInt64},
	colArgsBytesReceived: {"args.bytes_received", kindInt64}, colArgsTCPSeq: {"args.tcp_seq", kindInt32},
	colArgsPattern: {"args.pattern", kindBytes}, colArgsParentPID: {"args.parent_pid", kindInt32},
	colArgsParentInc: {"args.parent_incarnation", kindInt64}, colConnID: {"conn_id", kindBytes},
	colAttribution: {"attribution", kindDict}, colJavaThread: {"java.thread", kindBytes},
	colJavaStack:   {"java.stack.list.element", kindInt64},
	colJavaCarrier: {"java.carrier", kindBool}, colHTTPMethod: {"http.method", kindBytes},
	colHTTPScheme: {"http.scheme", kindBytes}, colHTTPHost: {"http.host", kindBytes},
	colHTTPPath: {"http.path", kindBytes}, colHTTPStatus: {"http.status", kindInt32},
	colHTTPReqSeq: {"http.req_seq", kindInt32}, colHTTPVersion: {"http.version", kindBytes},
	colHTTPReqHeaders:  {"http.req_headers.list.element", kindBytes},
	colHTTPRespHeaders: {"http.resp_headers.list.element", kindBytes},
	colHTTPReqBytes:    {"http.req_bytes", kindInt64}, colHTTPRespBytes: {"http.resp_bytes", kindInt64},
	colSessionBootID: {"session.boot_id", kindBytes}, colSessionFQDN: {"session.fqdn", kindBytes},
	colSessionKernel: {"session.kernel", kindBytes}, colSessionArch: {"session.arch", kindBytes},
	colStackID: {"stack_id", kindInt32}, colOtelTraceID: {"otel.trace_id", kindBytes},
	colOtelSpanID: {"otel.span_id", kindBytes}, colDNSHost: {"dns.host", kindBytes},
	colDNSAddrs: {"dns.addrs.list.element", kindBytes},
}

// eventColumns is the open row group of an events file.
type eventColumns struct {
	schema *parquetgo.Schema
	cols   [numEventColumns]column
	rows   int
}

// newEventColumns builds the columns and checks them against the schema
// parquet-go derives from schema.Event (see the column list above).
func newEventColumns() (*eventColumns, error) {
	e := &eventColumns{schema: parquetgo.SchemaOf(schema.Event{})}
	paths := e.schema.Columns()
	if len(paths) != numEventColumns {
		return nil, fmt.Errorf("segment: schema.Event has %d parquet columns, the writer knows %d",
			len(paths), numEventColumns)
	}
	for i, spec := range eventColumnSpecs {
		if err := e.initColumn(&e.cols[i], spec, paths[i]); err != nil {
			return nil, err
		}
	}
	return e, nil
}

func (e *eventColumns) initColumn(c *column, spec columnSpec, path []string) error {
	leaf, ok := e.schema.Lookup(path...)
	switch {
	case !ok || strings.Join(path, ".") != spec.path:
		return fmt.Errorf("segment: parquet column %v, the writer expects %s", path, spec.path)
	case leaf.Node.Type().Kind() != parquetgo.Kind(spec.kind.physical()):
		return fmt.Errorf("segment: parquet column %s is %v, the writer writes %v",
			spec.path, leaf.Node.Type().Kind(), parquetgo.Kind(spec.kind.physical()))
	}
	*c = column{path: slices.Clone(path), kind: spec.kind,
		maxDef: byte(leaf.MaxDefinitionLevel), maxRep: byte(leaf.MaxRepetitionLevel)}
	if spec.kind == kindDict {
		c.dict.idx = make(map[string]int32)
	}
	return nil
}

// add appends one event as a row. The mapping is parquet-go's (v0.29)
// for the struct tags: a nil pointer and an empty or nil list are null,
// while an optional non-pointer field (conn_id, java.thread,
// java.carrier, the http fields) is always present, its zero value
// included; uint16/uint32 are stored as int32 and uint64 as int64 bit
// patterns. TestEventColumnsMatchDeconstruct holds it to the
// library's own Deconstruct, value by value.
func (e *eventColumns) add(ev *schema.Event) {
	e.envelope(ev)
	e.args(&ev.Args)
	c := &e.cols
	c[colConnID].optString(ev.ConnID, true, 0)
	c[colAttribution].putDict(ev.Attribution)
	e.java(ev.Java)
	e.http(ev.HTTP)
	e.session(ev.Session)
	c[colStackID].optInt32(int32(deref(ev.StackID)), ev.StackID != nil, 0)
	e.otel(ev.Otel)
	e.dns(ev.DNS)
	e.rows++
}

func (e *eventColumns) envelope(ev *schema.Event) {
	c := &e.cols
	c[colSchemaVersion].putInt32(int32(ev.SchemaVersion))
	c[colProject].putDict(ev.Project)
	c[colSeq].putInt64(int64(ev.Seq))
	c[colBootNS].putInt64(int64(ev.BootNS))
	c[colWallNS].putInt64(int64(ev.WallNS))
	c[colHostID].putDict(ev.HostID)
	c[colPID].putInt32(int32(ev.PID))
	c[colPIDInc].putInt64(int64(ev.PIDInc))
	c[colTID].putInt32(int32(ev.TID))
	c[colComm].putDict(ev.Comm)
	c[colEvent].putDict(ev.Event)
	c[colFD].putInt32(ev.FD)
	c[colFDGen].putInt32(int32(ev.FDGen))
	c[colRet].putInt64(ev.Ret)
	c[colErr].putDict(ev.Err)
	c[colLatencyNS].putInt64(int64(ev.LatencyNS))
	c[colBytes].putInt64(int64(ev.Bytes))
}

func (e *eventColumns) args(a *schema.Args) {
	c := &e.cols
	optStr(&c[colArgsPath], a.Path)
	optStr(&c[colArgsNewPath], a.NewPath)
	optI32(&c[colArgsFlags], a.Flags)
	optI32(&c[colArgsCmd], a.Cmd)
	optI64(&c[colArgsOffset], a.Offset)
	optStr(&c[colArgsLocalAddr], a.LocalAddr)
	optI32(&c[colArgsLocalPort], a.LocalPort)
	optStr(&c[colArgsRemoteAddr], a.RemoteAddr)
	optI32(&c[colArgsRemotePort], a.RemotePort)
	optStr(&c[colArgsSockState], a.SockState)
	optStr(&c[colArgsMarker], a.Marker)
	optI64(&c[colArgsLost], a.Lost)
	optStr(&c[colArgsDropReason], a.DropReason)
	optStr(&c[colArgsEndReason], a.EndReason)
	c[colArgsArgv].listStrings(a.Argv, 0)
	optI32(&c[colArgsFD2], a.FD2)
	optI32(&c[colArgsFD2Gen], a.FD2Gen)
	optI32(&c[colArgsSignal], a.Signal)
	optI32(&c[colArgsTargetPID], a.TargetPID)
	optI32(&c[colArgsSenderPID], a.SenderPID)
	optI64(&c[colArgsCount], a.Count)
	optI32(&c[colArgsSndUna], a.SndUna)
	optI32(&c[colArgsRcvNxt], a.RcvNxt)
	optI64(&c[colArgsBytesAcked], a.BytesAcked)
	optI64(&c[colArgsBytesReceived], a.BytesReceived)
	optI32(&c[colArgsTCPSeq], a.TCPSeq)
	optStr(&c[colArgsPattern], a.Pattern)
	optI32(&c[colArgsParentPID], a.ParentPID)
	optI64(&c[colArgsParentInc], a.ParentInc)
}

// java, http, session, otel and dns write an optional nested object: a
// nil object is a null at level 0 in each of its leaves, a set one puts
// its leaves one level deeper (1 is "the object is there").
func (e *eventColumns) java(j *schema.Java) {
	c := &e.cols
	if j == nil {
		c[colJavaThread].null(0)
		c[colJavaStack].nullList(0)
		c[colJavaCarrier].null(0)
		return
	}
	c[colJavaThread].optString(j.Thread, true, 1)
	c[colJavaStack].listInt64s(j.Stack, 1)
	c[colJavaCarrier].optBool(j.Carrier, true, 1)
}

func (e *eventColumns) http(h *schema.HTTP) {
	c := &e.cols
	if h == nil {
		for i := colHTTPMethod; i <= colHTTPRespBytes; i++ {
			if i == colHTTPReqHeaders || i == colHTTPRespHeaders {
				c[i].nullList(0)
			} else {
				c[i].null(0)
			}
		}
		return
	}
	c[colHTTPMethod].optString(h.Method, true, 1)
	c[colHTTPScheme].optString(h.Scheme, true, 1)
	c[colHTTPHost].optString(h.Host, true, 1)
	c[colHTTPPath].optString(h.Path, true, 1)
	c[colHTTPStatus].optInt32(int32(h.Status), true, 1)
	c[colHTTPReqSeq].optInt32(int32(h.ReqSeq), true, 1)
	c[colHTTPVersion].optString(h.Version, true, 1)
	c[colHTTPReqHeaders].listStrings(h.ReqHeaders, 1)
	c[colHTTPRespHeaders].listStrings(h.RespHeaders, 1)
	c[colHTTPReqBytes].optInt64(int64(deref(h.ReqBytes)), h.ReqBytes != nil, 1)
	c[colHTTPRespBytes].optInt64(int64(deref(h.RespBytes)), h.RespBytes != nil, 1)
}

// session's fields are required inside the object, so a set session's
// leaves are at the object's level (1), which is their maxDef.
func (e *eventColumns) session(s *schema.Session) {
	c := &e.cols
	if s == nil {
		for i := colSessionBootID; i <= colSessionArch; i++ {
			c[i].null(0)
		}
		return
	}
	c[colSessionBootID].optString(s.BootID, true, 0)
	c[colSessionFQDN].optString(s.FQDN, true, 0)
	c[colSessionKernel].optString(s.Kernel, true, 0)
	c[colSessionArch].optString(s.Arch, true, 0)
}

func (e *eventColumns) otel(o *schema.Otel) {
	c := &e.cols
	c[colOtelTraceID].optString(derefOtel(o).TraceID, o != nil, 0)
	c[colOtelSpanID].optString(derefOtel(o).SpanID, o != nil, 0)
}

func (e *eventColumns) dns(d *schema.DNS) {
	c := &e.cols
	if d == nil {
		c[colDNSHost].null(0)
		c[colDNSAddrs].nullList(0)
		return
	}
	c[colDNSHost].optString(d.Host, true, 0)
	c[colDNSAddrs].listStrings(d.Addrs, 1)
}

// reset empties every column for the next row group.
func (e *eventColumns) reset() {
	for i := range e.cols {
		e.cols[i].reset()
	}
	e.rows = 0
}

// bufferedBytes is the open row group's unencoded size.
func (e *eventColumns) bufferedBytes() int {
	n := 0
	for i := range e.cols {
		n += e.cols[i].bufferedBytes()
	}
	return n
}

// optStr, optI32 and optI64 write an optional Args leaf from its
// pointer: nil is null at level 0 (Args itself is required).
func optStr(c *column, p *string) { c.optString(deref(p), p != nil, 0) }

func optI32[T ~int32 | ~uint32 | ~uint16](c *column, p *T) { c.optInt32(int32(deref(p)), p != nil, 0) }

func optI64[T ~int64 | ~uint64](c *column, p *T) { c.optInt64(int64(deref(p)), p != nil, 0) }

// deref is *p, or the zero value for nil.
func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

func derefOtel(o *schema.Otel) schema.Otel { return deref(o) }

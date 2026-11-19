package agentspool

// HTTP is one request an HTTP hook of the agent recorded (task 3b4,
// decision #22): written when the hooked method returned — HttpClient's
// send, HttpServer's exchange close, HttpURLConnection's response read —
// at ExitMono (CLOCK_MONOTONIC), on thread ThreadID (kernel tid KTID),
// from stack StackID. The agent redacted the path and the headers before
// writing them (decision #9); the joiner applies the tracer's policy
// again. Like a bracket it is covered by the watermarks: every HTTP
// record stamped before a WATERMARK's time is in the ring ahead of it.
type HTTP struct {
	ExitMono uint64
	StackID  uint64
	ThreadID uint64
	KTID     int32
	// Status is the response's status code, 0 when none came.
	Status int32
	Method string
	Scheme string
	// Host is the request URI's host on the client side, empty on the
	// server side.
	Host string
	// Path is the request target, path and query (query values masked).
	Path string
	// Version is the protocol, "HTTP/1.1" or "HTTP/2"; requests are
	// numbered per connection only over HTTP/1.
	Version string
	// Local and Remote are the request's socket ends, "ip:port" ("[ip]:
	// port" for IPv6), empty when the library no longer held them: the
	// joiner finds the connection (conn_id) by them.
	Local, Remote string
	// ReqHeaders and RespHeaders are "name: value", names lower-cased,
	// values redacted.
	ReqHeaders, RespHeaders []string
	// Server marks a request answered here (a server library's), not
	// sent: the two ends of one same-process loopback connection are
	// numbered apart.
	Server bool
	// Unordered marks a request whose record was stamped when the agent's
	// completion ran, after the app's own callbacks (HttpClient.sendAsync,
	// task wd4 review), not when its response arrived: its stamp says
	// nothing about the connection's request order, so it gets no ordinal
	// and stops its connection's count; it has no start either.
	Unordered bool
	// AsyncMark marks no request but a marker the agent wrote at the
	// entry of HttpClient.sendAsync, at ExitMono, before that request
	// could reach a connection (task wd4 review round 2): from then on the
	// JVM's java.net.http client ends are numbered no more, since an
	// async request's connection returns to the pool before its delayed
	// record, and a later request's record on it could take its ordinal.
	// Its strings are empty.
	AsyncMark bool
	// NetHTTP marks a request sent through java.net.http's HttpClient.
	NetHTTP bool
	// Exchanges is how many HTTP exchanges the request took (≥ 1): more
	// when the client library followed a redirect or answered an
	// authentication challenge itself, each a request the server saw.
	Exchanges uint32
	// Otel is the OTEL span current on the thread when the request was
	// recorded (task 4b4), zero when none was.
	Otel Otel
	// StartMono is when the library began the request (CLOCK_MONOTONIC,
	// task wd4): the entry of the client's send or the server's dispatch
	// that read it; 0 when the hook could not tie a start to the request.
	// Never after ExitMono.
	StartMono uint64
	// ReqBytes and RespBytes are the request's and the response's body
	// sizes as the library knew them (declared or counted), -1 unknown.
	ReqBytes, RespBytes int64
}

// decodeHTTP checks an HTTP record: a non-negative tid and status, at
// least the fixed strings, a request-header count that fits, no unknown
// flag, at least one exchange, OTEL ids either all set or none, a start
// not after the exit and sizes of -1 or more.
func decodeHTTP(rec []byte) (Record, error) {
	if len(rec) < httpHead {
		return nil, errMalformed
	}
	n, nreq, flags := le.Uint32(rec[40:]), le.Uint32(rec[44:]), le.Uint32(rec[48:])
	h := HTTP{ExitMono: le.Uint64(rec[8:]), StackID: le.Uint64(rec[16:]),
		ThreadID: le.Uint64(rec[24:]), KTID: int32(le.Uint32(rec[32:])),
		Status: int32(le.Uint32(rec[36:])), Server: flags&httpServer != 0,
		Unordered: flags&httpUnordered != 0, AsyncMark: flags&httpAsyncMark != 0,
		NetHTTP:   flags&httpNetHTTP != 0,
		Exchanges: le.Uint32(rec[52:]), StartMono: le.Uint64(rec[80:]),
		ReqBytes: int64(le.Uint64(rec[88:])), RespBytes: int64(le.Uint64(rec[96:]))}
	var otelOK bool
	h.Otel, otelOK = decodeOtel(rec, 56)
	if h.KTID < 0 || h.Status < 0 || n < httpFixed || nreq > n-httpFixed ||
		flags&^httpFlags != 0 || h.Exchanges == 0 || !otelOK ||
		h.StartMono > h.ExitMono || h.ReqBytes < -1 || h.RespBytes < -1 {
		return nil, errMalformed
	}
	s, err := decodeStrings(rec, httpHead, n)
	if err != nil {
		return nil, err
	}
	h.Method, h.Scheme, h.Host, h.Path, h.Version, h.Local, h.Remote =
		s[0], s[1], s[2], s[3], s[4], s[5], s[6]
	if nreq > 0 {
		h.ReqHeaders = s[httpFixed : httpFixed+nreq]
	}
	if rest := s[httpFixed+nreq:]; len(rest) > 0 {
		h.RespHeaders = rest
	}
	return h, nil
}

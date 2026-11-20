package internal_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	ibpf "iormesh/internal/bpf"
	"iormesh/internal/correlate"
	"iormesh/internal/schema"
)

// args.tcp_seq (M4 "Clock reconciliation", task xe4) from the real kernel:
// a child process (this test binary re-run as one, streamseq_child_test.go)
// moves known bytes over a loopback connection to this process, which
// traces the child through the loader with the data calls' handlers
// (internal/c/tcpseq.c), runs the records through the correlator and
// checks the events. A child, because the loader never traces the tracer's
// own process. TestStreamSeqValues pins every sent and received range of
// the seven data calls against the bytes moved and the ISNs; the
// concurrent senders test checks that a sequence number, when given, names
// exactly the calling thread's bytes; the folded test that a folded record
// never carries one. Root only.

// seqSyscalls are the syscalls these tests attach, enter and exit.
var seqSyscalls = []string{"connect", "write", "read", "writev", "readv", "sendto", "recvfrom",
	"sendfile64", "close"}

// seqChild is a running child scenario and the server end of its
// connection.
type seqChild struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	ln     *net.TCPListener
	conn   *net.TCPConn
}

// startSeqChild starts scenario in a child, which waits for the go line
// (seqChild.start) before it connects to the listener given in its
// environment: the trace must see the connect.
func startSeqChild(t *testing.T, scenario string) *seqChild {
	t.Helper()
	ln, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), seqChildEnv+"="+scenario, seqAddrEnv+"="+ln.Addr().String())
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	c := &seqChild{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout), ln: ln}
	t.Cleanup(func() { c.stop() })
	return c
}

func (c *seqChild) pid() uint32 { return uint32(c.cmd.Process.Pid) }

// start lets the child connect and accepts its connection.
func (c *seqChild) start(t *testing.T) {
	t.Helper()
	if _, err := io.WriteString(c.stdin, "go\n"); err != nil {
		t.Fatalf("start child: %v", err)
	}
	conn, err := c.ln.AcceptTCP()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	c.conn = conn
}

// result reads the child's result line and waits for it to exit.
func (c *seqChild) result(t *testing.T) seqResult {
	t.Helper()
	line, err := c.stdout.ReadBytes('\n')
	if err != nil {
		t.Fatalf("child result: %v", err)
	}
	var r seqResult
	if err := json.Unmarshal(line, &r); err != nil {
		t.Fatalf("child result %q: %v", line, err)
	}
	if err := c.cmd.Wait(); err != nil {
		t.Fatalf("child: %v", err)
	}
	return r
}

func (c *seqChild) stop() {
	_ = c.stdin.Close()
	_ = c.ln.Close()
	if c.conn != nil {
		_ = c.conn.Close()
	}
	if c.cmd.ProcessState == nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
}

// openSeqTracer traces pid with seqSyscalls and the TCP state tracepoint;
// aggregate false sends every read and write unfolded.
func openSeqTracer(t *testing.T, pid uint32, aggregate bool) *ibpf.Tracer {
	t.Helper()
	probes := []ibpf.Probe{{Program: "handle_inet_sock_set_state", Category: "sock",
		Event: "inet_sock_set_state"}}
	for _, sc := range seqSyscalls {
		for _, dir := range []string{"enter", "exit"} {
			ev := "sys_" + dir + "_" + sc
			probes = append(probes, ibpf.Probe{Program: "handle_" + ev, Category: "syscalls", Event: ev})
		}
	}
	// The control probes too: sched_process_exit flushes a thread's
	// folded calls when it exits (rwagg.c), else they reach userspace only
	// at the tracer's Close, after these tests stopped reading.
	probes = append(probes, ibpf.ControlProbes()...)
	return openTracerWith(t, ibpf.Config{Pids: []uint32{pid}, Probes: probes,
		NoAggregation: !aggregate})
}

// recordLog drains a tracer's records while the traffic runs, so the ring
// buffer never fills.
type recordLog struct {
	mu   sync.Mutex
	recs []any
	stop chan struct{}
	done chan struct{}
}

func collectRecords(tr *ibpf.Tracer) *recordLog {
	l := &recordLog{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(l.done)
		for {
			select {
			case batch, ok := <-tr.Records():
				if !ok {
					return
				}
				l.mu.Lock()
				l.recs = append(l.recs, batch...)
				l.mu.Unlock()
			case <-l.stop:
				return
			}
		}
	}()
	return l
}

// seqSink keeps what the correlator emits.
type seqSink struct{ events []schema.Event }

func (s *seqSink) Event(ev *schema.Event) error {
	s.events = append(s.events, *ev)
	return nil
}

func (s *seqSink) Close() error { return nil }

// events lets the last records arrive, stops collecting and returns the
// correlator's events of descriptor fd of pid, every event validated. The
// connection is unfused (its server end is not traced) and gets its conn
// id from the correlator's Close.
func (l *recordLog) events(t *testing.T, pid uint32, fd int32) []schema.Event {
	t.Helper()
	time.Sleep(300 * time.Millisecond)
	close(l.stop)
	<-l.done
	sink := &seqSink{}
	c := correlate.New(correlate.Config{Host: correlate.Host{ID: "h", Project: "p"}, Sink: sink})
	for _, r := range l.recs {
		if err := c.Handle(r); err != nil {
			t.Fatalf("correlate %+v: %v", r, err)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close correlator: %v", err)
	}
	var out []schema.Event
	for _, e := range sink.events {
		if err := e.Validate(); err != nil {
			t.Errorf("%s event invalid: %v", e.Event, err)
		}
		if e.PID == pid && e.FD == fd {
			out = append(out, e)
		}
	}
	return out
}

// established returns the ESTABLISHED tcp_state event among evs: its
// snd_una is the client's ISS + 1 and its rcv_nxt the server's, before
// any data, so the two are the first sequence numbers of each direction.
func established(t *testing.T, evs []schema.Event) (sndUna, rcvNxt uint32) {
	t.Helper()
	for _, e := range evs {
		if e.Event == schema.KindTCPState && e.Args.SockState != nil &&
			*e.Args.SockState == "ESTABLISHED" && e.Args.SndUna != nil {
			return *e.Args.SndUna, *e.Args.RcvNxt
		}
	}
	t.Fatalf("no ESTABLISHED tcp_state record with tcp_sock fields among %d events", len(evs))
	return 0, 0
}

// TestStreamSeqValues: each of the seven data calls, run with pauses that
// keep it out of any fold, carries the sequence number of its first byte:
// the client's first send its ISS + 1, its first receive the server's ISS
// + 1, and every later call where the bytes before it end.
func TestStreamSeqValues(t *testing.T) {
	requireRoot(t)
	child := startSeqChild(t, seqScenarioValues)
	log := collectRecords(openSeqTracer(t, child.pid(), false))
	child.start(t)
	if _, err := io.ReadFull(child.conn, make([]byte, sum(seqValueSends))); err != nil {
		t.Fatalf("read the child's sends: %v", err)
	}
	for _, n := range seqValueRecvs {
		time.Sleep(20 * time.Millisecond) // one chunk per child read
		if _, err := child.conn.Write(make([]byte, n)); err != nil {
			t.Fatalf("send to the child: %v", err)
		}
	}
	r := child.result(t)
	evs := log.events(t, child.pid(), r.Fd)
	sndUna, rcvNxt := established(t, evs)
	checkRanges(t, "sends", evs, schema.IsStreamSend, sndUna, seqValueSends)
	checkRanges(t, "receives", evs, schema.IsStreamReceive, rcvNxt, seqValueRecvs)
	for _, kind := range []string{schema.KindWrite, schema.KindWritev, schema.KindSendto,
		schema.KindSendfile64, schema.KindRead, schema.KindReadv, schema.KindRecvfrom} {
		if !hasKind(evs, kind) {
			t.Errorf("no %s event on the socket", kind)
		}
	}
}

func sum(ns []int) int {
	total := 0
	for _, n := range ns {
		total += n
	}
	return total
}

func hasKind(evs []schema.Event, kind string) bool {
	for _, e := range evs {
		if e.Event == kind {
			return true
		}
	}
	return false
}

// checkRanges requires the data events of kind among evs to be exactly
// the calls of want (their byte counts, in order), each carrying the
// sequence number where the bytes before it end, from first.
func checkRanges(t *testing.T, what string, evs []schema.Event, kind func(string) bool,
	first uint32, want []int) {
	t.Helper()
	var got []int
	next := first
	for _, e := range evs {
		if !kind(e.Event) || e.Bytes == 0 {
			continue
		}
		got = append(got, int(e.Bytes))
		switch {
		case e.Args.TCPSeq == nil:
			t.Errorf("%s: %s of %d bytes has no tcp_seq", what, e.Event, e.Bytes)
		case *e.Args.TCPSeq != next:
			t.Errorf("%s: %s of %d bytes has tcp_seq %#x, want %#x", what, e.Event, e.Bytes,
				*e.Args.TCPSeq, next)
		}
		next += uint32(e.Bytes)
	}
	if len(got) != len(want) || sum(got) != sum(want) {
		t.Errorf("%s: calls moved %v bytes, want %v", what, got, want)
	}
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			t.Errorf("%s: call %d moved %d bytes, want %d", what, i, got[i], want[i])
		}
	}
}

// TestStreamSeqConcurrentSenders: two threads of the child send on one
// blocking socket at once, so a send queued while the other waits for
// buffer space interleaves with it. Every write that carries tcp_seq names
// its own bytes - the received stream holds, at that offset, exactly the
// buffer slice the call wrote. Writes that interleaved carry none; how
// many did is logged, not required.
func TestStreamSeqConcurrentSenders(t *testing.T) {
	requireRoot(t)
	child := startSeqChild(t, seqScenarioConcurrent)
	log := collectRecords(openSeqTracer(t, child.pid(), false))
	child.start(t)
	stream := make([]byte, seqStreamBytes)
	if _, err := io.ReadFull(child.conn, stream); err != nil {
		t.Fatalf("read the stream: %v", err)
	}
	r := child.result(t)
	evs := log.events(t, child.pid(), r.Fd)
	first, _ := established(t, evs)
	byTid := map[uint32][]schema.Event{}
	for _, e := range evs {
		if e.Event == schema.KindWrite {
			byTid[e.TID] = append(byTid[e.TID], e)
		}
	}
	with, total := 0, 0
	for g, s := range r.Senders {
		w, n := checkSender(t, g, s, byTid[s.Tid], stream, first)
		with, total = with+w, total+n
	}
	t.Logf("%d of %d writes carried tcp_seq; the rest interleaved", with, total)
}

// checkSender matches sender g's write events, in order, to its calls and
// checks each tcp_seq against the stream. It returns how many carried one
// and how many there were.
func checkSender(t *testing.T, g int, s seqSender, evs []schema.Event, stream []byte,
	first uint32) (with, total int) {
	t.Helper()
	if len(evs) != len(s.Calls) {
		t.Fatalf("sender %d (tid %d): %d write events, want %d calls", g, s.Tid, len(evs), len(s.Calls))
	}
	for i, e := range evs {
		c := s.Calls[i]
		if int(e.Bytes) != c.N {
			t.Fatalf("sender %d call %d: event of %d bytes, call wrote %d", g, i, e.Bytes, c.N)
		}
		if e.Args.TCPSeq == nil {
			continue
		}
		with++
		off := int(*e.Args.TCPSeq - first)
		want := tagged(g, c.Buf)[c.Off : c.Off+c.N]
		if off+c.N > len(stream) || !bytes.Equal(stream[off:off+c.N], want) {
			t.Errorf("sender %d call %d: tcp_seq %#x (offset %d) does not name its %d bytes",
				g, i, *e.Args.TCPSeq, off, c.N)
		}
	}
	return with, len(evs)
}

// TestStreamSeqFolded: with the fold on, a hot write loop on a socket is
// sent as folded records, which never carry tcp_seq, while the loop's
// first calls, sent one by one, carry the sequence number where all bytes
// before them - folded ones included - end.
func TestStreamSeqFolded(t *testing.T) {
	requireRoot(t)
	child := startSeqChild(t, seqScenarioFolded)
	log := collectRecords(openSeqTracer(t, child.pid(), true))
	child.start(t)
	if _, err := io.ReadFull(child.conn, make([]byte, seqFoldWrites*seqFoldSize)); err != nil {
		t.Fatalf("read: %v", err)
	}
	r := child.result(t)
	evs := log.events(t, child.pid(), r.Fd)
	first, _ := established(t, evs)
	next, folded, single := first, 0, 0
	for _, e := range evs {
		if e.Event != schema.KindWrite {
			continue
		}
		switch {
		case e.Args.Count != nil:
			folded++
			if e.Args.TCPSeq != nil {
				t.Errorf("folded write of %d calls carries tcp_seq %#x", *e.Args.Count, *e.Args.TCPSeq)
			}
		case e.Args.TCPSeq == nil:
			t.Errorf("single write at %#x has no tcp_seq", next)
		case *e.Args.TCPSeq != next:
			t.Errorf("single write at %#x has tcp_seq %#x", next, *e.Args.TCPSeq)
		default:
			single++
		}
		next += uint32(e.Bytes)
	}
	if folded == 0 || single == 0 {
		t.Errorf("%d folded and %d single writes, want some of both", folded, single)
	}
	if moved := next - first; moved != seqFoldWrites*seqFoldSize {
		t.Errorf("write events moved %d bytes, want %d", moved, seqFoldWrites*seqFoldSize)
	}
}

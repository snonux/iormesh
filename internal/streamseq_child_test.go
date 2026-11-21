package internal_test

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The child side of streamseq_root_test.go: the test binary re-run with
// seqChildEnv set becomes a TCP client that, once its parent writes a line
// to its stdin, connects to seqAddrEnv, runs one scenario with plain
// blocking syscalls - so each call is one syscall the trace can be held
// to - and prints a seqResult line. TestMain dispatches it; with the
// variable unset the binary runs its tests as usual.

// Environment of a child scenario.
const (
	seqChildEnv = "IORMESH_SEQ_CHILD"
	seqAddrEnv  = "IORMESH_SEQ_ADDR"
)

// Scenarios.
const (
	seqScenarioValues     = "values"
	seqScenarioConcurrent = "concurrent"
	seqScenarioFolded     = "folded"
)

// The values scenario's byte counts, call by call: sends write, writev
// (two buffers), sendto and sendfile64; receives read, readv, recvfrom.
var (
	seqValueSends = []int{5, 24, 9, 300}
	seqValueRecvs = []int{7, 6, 4}
)

// The concurrent scenario: seqSenders threads each write seqSendsEach
// tagged buffers of seqSendSize bytes.
const (
	seqSenders     = 2
	seqSendsEach   = 100
	seqSendSize    = 64 << 10
	seqTagSize     = 4
	seqStreamBytes = seqSenders * seqSendsEach * seqSendSize
)

// The folded scenario: a hot loop of small writes.
const (
	seqFoldWrites = 200
	seqFoldSize   = 10
)

// seqResult is what a child reports: its socket's descriptor and, for the
// concurrent scenario, each sender's thread and write calls.
type seqResult struct {
	Fd      int32
	Senders []seqSender
}

// seqSender is one sender thread: its tid and its write calls in order.
type seqSender struct {
	Tid   uint32
	Calls []seqCall
}

// seqCall is one write syscall: which buffer, where in it, how much.
type seqCall struct{ Buf, Off, N int }

// TestMain also dispatches the namespace side of TestPidNSTracerConfined
// (pidns_root_test.go), whose variable carries the host bystander's pid,
// the multithreaded child of TestProcessExitGroupDead (exit_root_test.go),
// the namespace side of TestPidNSConnectEstablished
// (pidnsconnect_root_test.go), the self-renaming child of
// TestRenameReflected (rename_root_test.go) and the fstat(-1) pair of
// TestNegativeFdAcrossExec (badfd_root_test.go).
func TestMain(m *testing.M) {
	if host := os.Getenv(pidnsChildEnv); host != "" {
		os.Exit(pidnsChildMain(host))
	}
	if os.Getenv(exitChildEnv) != "" {
		os.Exit(exitChildMain())
	}
	if os.Getenv(pidnsConnectEnv) != "" {
		os.Exit(pidnsConnectMain())
	}
	if os.Getenv(renameChildEnv) != "" {
		os.Exit(renameChildMain())
	}
	if role := os.Getenv(badFDChildEnv); role != "" {
		os.Exit(badFDChildMain(role))
	}
	if scenario := os.Getenv(seqChildEnv); scenario != "" {
		if err := runSeqChild(scenario); err != nil {
			fmt.Fprintln(os.Stderr, "seq child:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runSeqChild waits for the go line, connects and runs scenario. The
// goroutine stays on one OS thread: the kernel folds a thread's hot loop
// per thread (rwagg.c), and a loop that hopped threads would leave one
// thread's folded calls pending - sent late, after the next thread's
// single calls - which the folded test's in-order byte count cannot follow.
func runSeqChild(scenario string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		return fmt.Errorf("wait for go: %w", err)
	}
	fd, err := seqConnect(os.Getenv(seqAddrEnv))
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	r := seqResult{Fd: int32(fd)}
	switch scenario {
	case seqScenarioValues:
		err = childValues(fd)
	case seqScenarioConcurrent:
		r.Senders, err = childConcurrent(fd)
	case seqScenarioFolded:
		err = childFolded(fd)
	default:
		err = fmt.Errorf("unknown scenario %q", scenario)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(r)
}

// seqConnect opens a blocking IPv4 TCP socket connected to addr.
func seqConnect(addr string) (int, error) {
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		return -1, err
	}
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	if err != nil {
		return -1, err
	}
	if err := unix.Connect(fd, &unix.SockaddrInet4{Addr: ap.Addr().As4(), Port: int(ap.Port())}); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

// childValues sends through write, writev, sendto and sendfile64 and
// receives through read, readv and recvfrom, one call each, pausing after
// every send so none folds. A receive takes exactly its buffer, which the
// parent sends as one chunk per call.
func childValues(fd int) error {
	file, err := seqFile(seqValueSends[3])
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	sends := []func() (int, error){
		func() (int, error) { return unix.Write(fd, make([]byte, 5)) },
		func() (int, error) { return unix.Writev(fd, [][]byte{make([]byte, 11), make([]byte, 13)}) },
		func() (int, error) { return seqSendto(fd, 9) },
		func() (int, error) { return unix.Sendfile(fd, int(file.Fd()), nil, seqValueSends[3]) },
	}
	recvs := []func() (int, error){
		func() (int, error) { return unix.Read(fd, make([]byte, 7)) },
		func() (int, error) { return unix.Readv(fd, [][]byte{make([]byte, 6)}) },
		func() (int, error) { n, _, err := unix.Recvfrom(fd, make([]byte, 4), 0); return n, err },
	}
	if err := seqCalls(sends, seqValueSends); err != nil {
		return err
	}
	return seqCalls(recvs, seqValueRecvs)
}

func seqSendto(fd, n int) (int, error) {
	if err := unix.Sendto(fd, make([]byte, n), 0, nil); err != nil {
		return 0, err
	}
	return n, nil
}

// seqCalls runs each call, requires it to move its count of bytes, and
// pauses after it.
func seqCalls(calls []func() (int, error), want []int) error {
	for i, call := range calls {
		n, err := call()
		if err != nil || n != want[i] {
			return fmt.Errorf("call %d moved %d bytes, want %d: %v", i, n, want[i], err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return nil
}

// seqFile returns an open temporary file of n bytes, already unlinked.
func seqFile(n int) (*os.File, error) {
	f, err := os.CreateTemp("", "iormesh-seq")
	if err != nil {
		return nil, err
	}
	_ = os.Remove(f.Name())
	if _, err := f.Write(make([]byte, n)); err != nil {
		_ = f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, 0); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// childConcurrent runs the senders on their own OS threads at once.
func childConcurrent(fd int) ([]seqSender, error) {
	senders := make([]seqSender, seqSenders)
	errs := make([]error, seqSenders)
	var wg sync.WaitGroup
	for g := range senders {
		wg.Go(func() { errs[g] = sendTagged(fd, g, &senders[g]) })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return senders, nil
}

// sendTagged writes sender g's buffers, retrying a partial write (a
// signal ends a blocking send early) with the rest, and logs every call.
func sendTagged(fd, g int, s *seqSender) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	s.Tid = uint32(unix.Gettid())
	for k := range seqSendsEach {
		buf := tagged(g, k)
		for off := 0; off < len(buf); {
			n, err := unix.Write(fd, buf[off:])
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				return fmt.Errorf("sender %d write %d: %w", g, k, err)
			}
			s.Calls = append(s.Calls, seqCall{Buf: k, Off: off, N: n})
			off += n
		}
	}
	return nil
}

// tagged is sender g's buffer k: its tag repeated, so any slice of it
// says whose bytes it holds.
func tagged(g, k int) []byte {
	buf := make([]byte, seqSendSize)
	for i := 0; i < len(buf); i += seqTagSize {
		binary.LittleEndian.PutUint32(buf[i:], uint32(g<<24|k))
	}
	return buf
}

// childFolded writes the hot loop.
func childFolded(fd int) error {
	buf := make([]byte, seqFoldSize)
	for i := range seqFoldWrites {
		if n, err := unix.Write(fd, buf); err != nil || n != seqFoldSize {
			return fmt.Errorf("write %d: %d bytes, %v", i, n, err)
		}
	}
	return nil
}

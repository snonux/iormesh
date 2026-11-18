package integrationtests

// Decision #25's fold of hot read/write loops (M2 step 11, internal/c/
// rwagg.c), end to end: the syscall burst's 1-byte write/read loop must be
// folded - or the tracer could not keep up with it - and folding must keep
// every call: the counts of the write and read events on the burst's files
// add up to exactly the ops the burst made, on every thread.

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"iormesh/internal/schema"
)

// burstOps and burstThreads size the traced burst: enough calls per thread
// that most are folded, few enough to finish in a second or two.
const (
	burstOps     = 20000
	burstThreads = 2
)

// fdInstance names one fd instance of the traced process.
type fdInstance struct {
	pid   uint32
	fd    int32
	fdGen uint32
}

// TestSyscallBurstFoldsEveryCall: a traced syscall-burst loses no record,
// is folded (events with args.count), and its folded and single write and
// read events on its own files account for every one of its ops.
func TestSyscallBurstFoldsEveryCall(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	run := traceJudged(t, h, func(h *Harness) ([]*Workload, error) {
		w, err := h.StartWorkload("syscall-burst",
			"--ops", strconv.Itoa(burstOps), "--threads", strconv.Itoa(burstThreads))
		return []*Workload{w}, err
	})
	assertOnlyTracedPIDs(t, run.Trace.Events, run.pids)

	writes, reads := burstFiles(run.Trace.Events)
	if len(writes) != burstThreads || len(reads) != burstThreads {
		t.Fatalf("opened %d write and %d read burst files, want %d each", len(writes), len(reads), burstThreads)
	}
	var wrote, read, folded uint64
	for _, e := range run.Trace.Events {
		calls := uint64(1)
		if e.Args.Count != nil {
			calls = *e.Args.Count
			folded++
		}
		in := fdInstance{e.PID, e.FD, e.FDGen}
		switch {
		case e.Event == schema.KindWrite && writes[in]:
			wrote += calls
			assertOneByteCalls(t, e, calls)
		case e.Event == schema.KindRead && reads[in]:
			read += calls
			assertOneByteCalls(t, e, calls)
		}
	}
	if wrote != burstOps || read != burstOps {
		t.Fatalf("the trace accounts for %d writes and %d reads, want %d each", wrote, read, burstOps)
	}
	if folded == 0 {
		t.Fatal("no event carries args.count: the burst was not folded")
	}
}

// burstFiles returns the fd instances the burst opened its files on, for
// writing and for reading.
func burstFiles(events []schema.Event) (writes, reads map[fdInstance]bool) {
	writes, reads = map[fdInstance]bool{}, map[fdInstance]bool{}
	for _, e := range events {
		if e.Event != schema.KindOpenat || e.Ret < 0 || e.Args.Path == nil ||
			!strings.HasPrefix(filepath.Base(*e.Args.Path), "syscall-burst-") {
			continue
		}
		in := fdInstance{e.PID, e.FD, e.FDGen}
		if e.Args.Flags != nil && *e.Args.Flags&0o3 != 0 {
			writes[in] = true
		} else {
			reads[in] = true
		}
	}
	return writes, reads
}

// assertOneByteCalls checks a burst event's sums: each of its calls moved
// one byte, so bytes and ret equal the call count.
func assertOneByteCalls(t *testing.T, e schema.Event, calls uint64) {
	t.Helper()
	if e.Bytes != calls || e.Ret != int64(calls) {
		t.Fatalf("%s seq %d: %d calls but bytes %d, ret %d", e.Event, e.Seq, calls, e.Bytes, e.Ret)
	}
}

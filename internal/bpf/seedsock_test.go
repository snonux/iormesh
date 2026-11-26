package bpf

import (
	"errors"
	"syscall"
	"testing"
)

// iterReads is a reader returning one scripted result per Read.
type iterReads struct {
	n    []int
	errs []error
	at   int
}

func (r *iterReads) Read([]byte) (int, error) {
	i := r.at
	r.at++
	return r.n[i], r.errs[i]
}

// TestDrainIter: an iterator's reader is read until its zero-byte end,
// through data, an interrupted read and an EAGAIN — the kernel's
// bpf_seq_read returns it after visiting MAX_ITER_OBJECTS (1M) objects
// without output, which the seed programs never write, and the walk
// resumes on the next read; any other error ends the run with that
// error (task cm4).
func TestDrainIter(t *testing.T) {
	r := &iterReads{n: []int{10, 0, 3, 0}, errs: []error{nil, syscall.EINTR, nil, nil}}
	if err := drainIter(r); err != nil || r.at != 4 {
		t.Fatalf("drain = %v after %d reads, want nil after 4", err, r.at)
	}
	r = &iterReads{n: []int{0, 0, 3, 0}, errs: []error{syscall.EAGAIN, syscall.EAGAIN, nil, nil}}
	if err := drainIter(r); err != nil || r.at != 4 {
		t.Fatalf("drain through EAGAIN = %v after %d reads, want nil after 4", err, r.at)
	}
	boom := errors.New("boom")
	r = &iterReads{n: []int{10, 0}, errs: []error{nil, boom}}
	if err := drainIter(r); !errors.Is(err, boom) {
		t.Fatalf("drain = %v, want the read's error", err)
	}
}

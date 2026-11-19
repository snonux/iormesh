package bpf

import (
	"errors"
	"testing"
)

// TestLossesCountSkippedPrograms: a program run the kernel skipped is a
// lost record like a failed reservation - in Total, which drop events are
// made of, and in the kernel-side figure the summary line reports (task
// vc4: skipped read/write handlers showed up only as unpaired exits).
func TestLossesCountSkippedPrograms(t *testing.T) {
	l := Losses{Kernel: 2, Missed: 5, Short: 1}
	if l.Total() != 8 || l.KernelSide() != 7 {
		t.Fatalf("Total %d, KernelSide %d: want 8 and 7", l.Total(), l.KernelSide())
	}
}

// TestReadProgMisses sums the attached programs' misses and fails on an
// unreadable one: a loss counter that cannot be read must not read as 0.
func TestReadProgMisses(t *testing.T) {
	misses := map[int]uint64{3: 710, 4: 1503, 5: 0}
	read := func(fd int) (uint64, error) {
		n, ok := misses[fd]
		if !ok {
			return 0, errors.New("no such program")
		}
		return n, nil
	}
	if n, err := readProgMisses([]int{3, 4, 5}, read); err != nil || n != 2213 {
		t.Fatalf("readProgMisses = %d, %v; want 2213", n, err)
	}
	if _, err := readProgMisses([]int{3, 9}, read); err == nil {
		t.Fatal("an unreadable program read as no misses")
	}
}

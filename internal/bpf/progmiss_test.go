package bpf

import (
	"errors"
	"testing"
)

// TestLossesKeepSkippedProgramsApart: a program run the kernel skipped is
// possible loss, counted host-wide before the target filter, so it is not
// in Total - the lost records drop events and the summary report (task
// vd4) - while failed reservations and undecodable records are.
func TestLossesKeepSkippedProgramsApart(t *testing.T) {
	tests := []struct {
		name string
		l    Losses
		want uint64
	}{
		{"skips only", Losses{Missed: 691}, 0},
		{"ring drops", Losses{Kernel: 2, Missed: 5}, 2},
		{"every kind", Losses{Kernel: 2, Missed: 5, Unknown: 1, Malformed: 3, Short: 1}, 7},
	}
	for _, tt := range tests {
		if got := tt.l.Total(); got != tt.want {
			t.Errorf("%s: Total() = %d, want %d", tt.name, got, tt.want)
		}
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

package integrationtests

// Adapted from ior integrationtests/kernelloss_test.go @ b21afc50328c.
// Changes: iormesh's one summary line instead of ior's statistics block,
// with its skipped program runs (task vd4).

import (
	"errors"
	"testing"
)

func TestParseKernelLoss(t *testing.T) {
	tests := []struct {
		name    string
		logged  string
		want    KernelLoss
		wantErr bool
	}{
		{name: "clean run",
			logged: "iormesh: attached\niormesh: stopped (targets exited); lost records 0 (kernel 0), skipped runs 0, unpaired exits 2, unreadable paths 0\n",
			want:   KernelLoss{Unpaired: 2}},
		{name: "kernel and decode loss",
			logged: "iormesh: stopped (duration); lost records 7 (kernel 5), skipped runs 0, unpaired exits 1, unreadable paths 3",
			want:   KernelLoss{Lost: 7, Kernel: 5, Unpaired: 1, NoPath: 3}},
		{name: "skipped runs apart from lost records",
			logged: "iormesh: stopped (targets_exited); lost records 0 (kernel 0), skipped runs 691, unpaired exits 0, unreadable paths 0",
			want:   KernelLoss{Skipped: 691}},
		// The pre-vd4 line lacks the skipped figure: reading it as 0 would
		// hide possible loss, so it is no summary line at all.
		{name: "line without skipped runs",
			logged:  "iormesh: stopped (duration); lost records 0 (kernel 0), unpaired exits 0, unreadable paths 0",
			wantErr: true},
		{name: "no summary line", logged: "iormesh: attached\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseKernelLoss(tt.logged)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseKernelLoss() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ParseKernelLoss() = %+v, want %+v", got, tt.want)
			}
		})
	}
	loss := KernelLoss{Lost: 7, Kernel: 5}
	if !loss.Any() || loss.Undecodable() != 2 {
		t.Errorf("%+v: Any() = %v, Undecodable() = %d; want true, 2", loss, loss.Any(), loss.Undecodable())
	}
	if (KernelLoss{Lost: 2, Unpaired: 9}).Any() {
		t.Error("decode loss and unpaired exits alone must not count as kernel loss")
	}
	// A skip may have been a target's call: the run cannot be judged.
	skipped := KernelLoss{Skipped: 691}
	if !skipped.Any() || skipped.Undecodable() != 0 {
		t.Errorf("%+v: Any() = %v, Undecodable() = %d; want true, 0", skipped, skipped.Any(), skipped.Undecodable())
	}
}

func TestFirstRunWithoutKernelLoss(t *testing.T) {
	lossy := KernelLoss{Lost: 1, Kernel: 1}
	tests := []struct {
		name     string
		losses   []KernelLoss
		want     int // the run whose result is returned; 0 = none
		wantLost int
	}{
		{"first run clean", []KernelLoss{{}}, 1, 0},
		{"second run clean", []KernelLoss{lossy, {}}, 2, 1},
		{"every run lossy", []KernelLoss{lossy, lossy, lossy}, 0, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			got, lost, err := FirstRunWithoutKernelLoss(3, func() (int, KernelLoss, error) {
				calls++
				return calls, tt.losses[calls-1], nil
			})
			if err != nil || got != tt.want || len(lost) != tt.wantLost {
				t.Errorf("got run %d, %d lost, err %v; want run %d, %d lost", got, len(lost), err, tt.want, tt.wantLost)
			}
		})
	}
	boom := errors.New("boom")
	if _, _, err := FirstRunWithoutKernelLoss(3, func() (int, KernelLoss, error) { return 0, KernelLoss{}, boom }); !errors.Is(err, boom) {
		t.Errorf("a failed run must end the retries with its error, got %v", err)
	}
}

package integrationtests

// Adapted from ior integrationtests/kernelloss_test.go @ b21afc50328c.
// Changes: iormesh's one summary line instead of ior's statistics block.

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
			logged: "iormesh: attached\niormesh: stopped (targets exited); lost records 0 (kernel 0), unpaired exits 2, unreadable paths 0\n",
			want:   KernelLoss{Unpaired: 2}},
		{name: "kernel and decode loss",
			logged: "iormesh: stopped (duration); lost records 7 (kernel 5), unpaired exits 1, unreadable paths 3",
			want:   KernelLoss{Lost: 7, Kernel: 5, Unpaired: 1, NoPath: 3}},
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

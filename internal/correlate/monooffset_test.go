package correlate

import (
	"errors"
	"testing"
)

// TestBestOffsetSkipsPreemptedTriples: a triple preempted between its
// reads (a wide window, its midpoint far off) must not set the offset
// while a narrow one exists — on a loaded host a single such sample made
// bracketOf judge a batch of joined events unbracketed (task se4).
func TestBestOffsetSkipsPreemptedTriples(t *testing.T) {
	const boot = 1_000_000_000 // true offset: boot − mono = 1 s
	triples := [][3]int64{
		{5_000, boot + 5_100, 5_000 + 6_000_000},              // preempted 6 ms after its boot read: ~3 ms off
		{10_000_000, boot + 10_000_020, 10_000_040},           // narrow: 40 ns window
		{20_000_000, boot + 20_000_000, 20_000_000 + 900_000}, // wide again
	}
	i := 0
	read := func() (int64, int64, int64, error) {
		tr := triples[i%len(triples)]
		i++
		return tr[0], tr[1], tr[2], nil
	}
	got, err := bestOffset(len(triples), read)
	if err != nil {
		t.Fatal(err)
	}
	if got != boot {
		t.Fatalf("offset %d, want %d (the narrow triple's)", got, boot)
	}
}

// TestBestOffsetError: a failing clock read is returned, never an offset
// built from a partial sample.
func TestBestOffsetError(t *testing.T) {
	boom := errors.New("clock")
	if _, err := bestOffset(4, func() (int64, int64, int64, error) { return 0, 0, 0, boom }); !errors.Is(err, boom) {
		t.Fatalf("err %v, want %v", err, boom)
	}
}

// TestMonoOffsetIsStable: two real samples agree within a few µs (the
// clocks differ only by time suspended), which a single preemptible
// triple did not guarantee.
func TestMonoOffsetIsStable(t *testing.T) {
	a, err := MonoOffset()
	if err != nil {
		t.Fatal(err)
	}
	b, err := MonoOffset()
	if err != nil {
		t.Fatal(err)
	}
	if d := a - b; d > 50_000 || d < -50_000 {
		t.Fatalf("offsets %d and %d differ by %d ns", a, b, d)
	}
}

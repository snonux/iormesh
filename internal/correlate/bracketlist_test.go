package correlate

import (
	"math/rand/v2"
	"slices"
	"testing"

	"iormesh/internal/agentspool"
)

// TestBracketListMatchesSlice holds the chunked list to the plain sorted
// slice it replaced (task ee4): random adds — mostly in exit order, some
// out of it, as a carrier's virtual threads publish — and front drops,
// with chunks of four so every operation crosses chunk boundaries.
func TestBracketListMatchesSlice(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	bl := newBracketList(2)
	var model []agentspool.Bracket
	exit := uint64(100)
	for step := range 5000 {
		switch op := r.IntN(10); {
		case op < 7:
			exit += uint64(r.IntN(3))
			b := agentspool.Bracket{ExitMono: exit - uint64(r.IntN(2)*r.IntN(5)), StackID: uint64(step)}
			bl.add(&b)
			i := len(model)
			for i > 0 && model[i-1].ExitMono > b.ExitMono {
				i--
			}
			model = slices.Insert(model, i, b)
		default:
			k := r.IntN(min(len(model), 6) + 1)
			bl.dropFront(k)
			model = model[k:]
		}
		if bl.len() != len(model) {
			t.Fatalf("step %d: len %d, want %d", step, bl.len(), len(model))
		}
		for i := range model {
			if *bl.at(i) != model[i] {
				t.Fatalf("step %d: at(%d) = %+v, want %+v", step, i, *bl.at(i), model[i])
			}
		}
	}
	if len(bl.chunks) > bl.n/4+2 {
		t.Errorf("%d chunks for %d brackets: dropped chunks are kept", len(bl.chunks), bl.n)
	}
}

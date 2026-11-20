package correlate

import "iormesh/internal/agentspool"

// bracketChunkShift sets a bracketList's chunk length, 1<<shift
// brackets (1024, ~80 KiB).
const bracketChunkShift = 10

// bracketList is one thread's brackets in exit order, with a search hint:
// the index innermost found last. A thread's events reach the join in
// order and their natives' entries never go back in time, so the next
// search starts there (exitSearch) — a step or two instead of a search
// over Retain's worth of brackets (task ud4). The hint is only ever a
// guess, checked before it is used: inserts and prunes may move it.
//
// The brackets live in fixed-size chunks, not one slice (task ee4): a
// busy thread keeps Retain's worth of them (10 s, a million on the S3
// burst), and a single slice regrew by copying all of them, ~8 % of the
// joined event's cost. Appending fills the last chunk; pruning drops
// whole chunks off the front and keeps one as a spare for the next
// append, so a thread in a steady state allocates nothing.
type bracketList struct {
	chunks [][]agentspool.Bracket // each 1<<shift long; live from chunks[0][head]
	spare  []agentspool.Bracket   // a pruned chunk, reused by the next append
	head   int
	n      int
	shift  uint
	hint   int
}

func newBracketList(shift uint) *bracketList { return &bracketList{shift: shift} }

// len is the number of brackets in the list.
func (l *bracketList) len() int { return l.n }

// at is the i-th bracket in exit order, 0 <= i < len.
func (l *bracketList) at(i int) *agentspool.Bracket {
	i += l.head
	return &l.chunks[i>>l.shift][i&(1<<l.shift-1)]
}

// add inserts b in exit order. Ring order is exit order per tid, so this
// appends; the backward walk covers the carriers of virtual threads,
// where two of them may publish out of order.
func (l *bracketList) add(b *agentspool.Bracket) {
	l.grow()
	i := l.n - 1
	for ; i > 0 && l.at(i-1).ExitMono > b.ExitMono; i-- {
		*l.at(i) = *l.at(i - 1)
	}
	*l.at(i) = *b
}

// grow makes room for one more bracket at the end.
func (l *bracketList) grow() {
	if l.head+l.n == len(l.chunks)<<l.shift {
		c := l.spare
		if c == nil {
			c = make([]agentspool.Bracket, 1<<l.shift)
		}
		l.spare = nil
		l.chunks = append(l.chunks, c)
	}
	l.n++
}

// dropFront forgets the first k brackets, recycling chunks left empty.
func (l *bracketList) dropFront(k int) {
	size := 1 << l.shift
	l.head += k
	l.n -= k
	l.hint = max(l.hint-k, 0)
	for l.head >= size {
		// A Bracket holds no pointers, so the spare needs no clearing:
		// every slot is written before it is read again.
		l.spare = l.chunks[0]
		l.chunks[0] = nil
		l.chunks = l.chunks[1:]
		l.head -= size
	}
}

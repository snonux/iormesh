package normalize

// symbols hands out ordinals per kind in order of first appearance: the
// first PID seen becomes 1, the second 2, and so on. Ordinals are what
// make two runs comparable — the raw values differ per run, the order in
// which a deterministic scenario first touches them does not.
type symbols struct {
	next map[string]int // kind → last ordinal handed out
	seen map[string]int // kind + "\x00" + raw key → ordinal
}

func newSymbols() *symbols {
	return &symbols{next: map[string]int{}, seen: map[string]int{}}
}

// ord returns the ordinal of key within kind, assigning the next free one
// on first sight.
func (s *symbols) ord(kind, key string) int {
	k := kind + "\x00" + key
	if n, ok := s.seen[k]; ok {
		return n
	}
	s.next[kind]++
	s.seen[k] = s.next[kind]
	return s.next[kind]
}

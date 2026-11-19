package agentspool

// Losses counts every record the agent meant to deliver that did not
// arrive, cumulative since the ring was created, in the shape of
// bpf.Losses so the correlator can turn deltas into drop events (PLAN.md,
// "Trace output contract": loss is recorded, never silent).
type Losses struct {
	// Dropped* are records the agent did not write because the reader
	// was a whole ring behind (counted by the agent in the header, per
	// type). A dropped Thread or Stack is retried by the agent, so each
	// failed attempt counts; DroppedStacks also counts the sightings of
	// stacks past the agent's table cap, whose frames are never written.
	DroppedBrackets, DroppedStacks, DroppedThreads, DroppedWatermarks uint64
	// DroppedHooks are hook-table rows the agent could not write at
	// install; that hook's brackets then cannot be joined.
	DroppedHooks uint64
	// DroppedOther is the per-type counters of types this reader does
	// not know (a newer agent would bump Version first; kept for honesty).
	DroppedOther uint64
	// DroppedBytes is the bytes of all dropped records.
	DroppedBytes uint64
	// Abandoned are records whose fill threw inside the agent (e.g. a
	// StackOverflowError in the target's thread); they were turned into
	// padding.
	Abandoned uint64
	// Unknown and Malformed arrived but could not be decoded: a type
	// this reader does not know, or a body that breaks the format.
	Unknown, Malformed uint64
}

// Dropped is every record the agent dropped, all types.
func (l Losses) Dropped() uint64 {
	return l.DroppedBrackets + l.DroppedStacks + l.DroppedThreads + l.DroppedWatermarks +
		l.DroppedHooks + l.DroppedOther
}

// Total is every lost record, agent- and reader-side.
func (l Losses) Total() uint64 {
	return l.Dropped() + l.Abandoned + l.Unknown + l.Malformed
}

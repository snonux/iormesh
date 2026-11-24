package agentspool

// Losses counts every record the agent meant to deliver that did not
// arrive, cumulative since the ring was created, in the shape of
// bpf.Losses so the correlator can turn deltas into drop events
// (docs/output-contract.md: loss is recorded, never silent).
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
	// DroppedHTTP are HTTP records (task 3b4): requests the agent saw
	// but could not deliver. Once any is lost, the joiner stops numbering
	// that JVM's requests (req_seq), which would shift silently.
	DroppedHTTP uint64
	// DroppedDNS are DNS records (task wd4): host-to-address mappings the
	// agent saw but could not deliver; the agent does not resend them.
	DroppedDNS uint64
	// DroppedClasses are CLASS records (task fe4): the agent retries one
	// when a later new stack names the class, so each failed attempt
	// counts; a frame whose class never arrived has no class_id.
	DroppedClasses uint64
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
		l.DroppedHooks + l.DroppedHTTP + l.DroppedDNS + l.DroppedClasses + l.DroppedOther
}

// Total is every lost record, agent- and reader-side.
func (l Losses) Total() uint64 {
	return l.Dropped() + l.Abandoned + l.Unknown + l.Malformed
}

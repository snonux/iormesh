package types

// Recycle returns a decoded record to its type's pool, if it has one. The
// fast decoders take every record from a sync.Pool; a record never put
// back makes each Get allocate, and on a syscall burst those allocations
// and the GC they cause were a fifth of the tracer's userspace time per
// event (task wc4, decision #25). The record's last consumer calls it
// once it is done: nothing may hold the pointer afterwards, since the next
// decode overwrites it. A nil or pool-less value is ignored.
func Recycle(rec any) {
	if r, ok := rec.(interface{ Recycle() }); ok {
		r.Recycle()
	}
}

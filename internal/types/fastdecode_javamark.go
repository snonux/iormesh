package types

import "encoding/binary"

// javaMarkEventSize is sizeof(struct java_mark_event) (types.h): no
// padding, so the kernel and binary.Write agree on it.
const javaMarkEventSize = 40

// NewJavaMarkEventFast decodes a Java mark (javamark.c, task td4) field
// by field, as NewJavaMarkEvent's binary.Read does by reflection: with
// -spool every hooked call of a JVM sends one ahead of its enter, so on
// the S3 burst under the agent the reflective decode was ~0.2 µs of the
// tracer's CPU per event (task ud4, decision #25). Like binary.Read it
// rejects a short payload (nil) and ignores bytes past the struct.
func NewJavaMarkEventFast(raw []byte) *JavaMarkEvent {
	if len(raw) < javaMarkEventSize {
		return nil
	}
	j := poolOfJavaMarkEvents.Get().(*JavaMarkEvent)
	j.EventType = EventType(binary.LittleEndian.Uint32(raw[0:4]))
	j.TraceId = TraceId(binary.LittleEndian.Uint32(raw[4:8]))
	j.Time = binary.LittleEndian.Uint64(raw[8:16])
	j.Pid = binary.LittleEndian.Uint32(raw[16:20])
	j.Tid = binary.LittleEndian.Uint32(raw[20:24])
	j.Cookie = binary.LittleEndian.Uint64(raw[24:32])
	j.Entry = binary.LittleEndian.Uint64(raw[32:40])
	return j
}

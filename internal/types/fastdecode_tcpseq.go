package types

import "encoding/binary"

// tcpSeqEventSize is sizeof(struct tcp_seq_event) (types.h): no padding,
// so the kernel and binary.Write agree on it.
const tcpSeqEventSize = 48

// NewTcpSeqEventFast decodes a TCP data call's exit record with its
// sequence number (tcpseq.c, task xe4) field by field, as NewTcpSeqEvent's
// binary.Read does by reflection: it is the exit record of every single
// TCP send and receive, so it is decoded the way the hot records are
// (decision #25; NewJavaMarkEventFast). Like
// binary.Read it rejects a short payload (nil) and ignores bytes past the
// struct.
func NewTcpSeqEventFast(raw []byte) *TcpSeqEvent {
	if len(raw) < tcpSeqEventSize {
		return nil
	}
	s := poolOfTcpSeqEvents.Get().(*TcpSeqEvent)
	s.EventType = EventType(binary.LittleEndian.Uint32(raw[0:4]))
	s.TraceId = TraceId(binary.LittleEndian.Uint32(raw[4:8]))
	s.Time = binary.LittleEndian.Uint64(raw[8:16])
	s.Ret = int64(binary.LittleEndian.Uint64(raw[16:24]))
	s.Pid = binary.LittleEndian.Uint32(raw[24:28])
	s.Tid = binary.LittleEndian.Uint32(raw[28:32])
	s.Sk = binary.LittleEndian.Uint64(raw[32:40])
	s.Seq = binary.LittleEndian.Uint32(raw[40:44])
	s.Reserved = binary.LittleEndian.Uint32(raw[44:48])
	return s
}

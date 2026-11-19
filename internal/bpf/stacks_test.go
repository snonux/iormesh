package bpf

import (
	"encoding/binary"
	"slices"
	"testing"
)

// TestParseStack: a stack_map value ends at its first zero frame (the
// kernel zero-fills a shorter stack) and never exceeds the map's depth.
func TestParseStack(t *testing.T) {
	raw := make([]byte, stackDepth*8)
	for i, ip := range []uint64{0x401000, 0x402000, 0x403000} {
		binary.LittleEndian.PutUint64(raw[i*8:], ip)
	}
	if got := parseStack(raw); !slices.Equal(got, []uint64{0x401000, 0x402000, 0x403000}) {
		t.Fatalf("parseStack = %#x", got)
	}
	full := make([]byte, (stackDepth+4)*8)
	for i := range stackDepth + 4 {
		binary.LittleEndian.PutUint64(full[i*8:], uint64(i+1))
	}
	if got := parseStack(full); len(got) != stackDepth {
		t.Fatalf("%d frames from an over-long value, want %d", len(got), stackDepth)
	}
	if got := parseStack(raw[:5]); len(got) != 0 {
		t.Fatalf("a value shorter than one frame gave %#x", got)
	}
}

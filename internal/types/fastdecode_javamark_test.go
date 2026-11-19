package types

import (
	"testing"
)

// TestNewJavaMarkEventFastMatchesSlow holds the fast decoder to the
// reflective one on the kernel's layout, on a longer payload, and on a
// short one (both nil).
func TestNewJavaMarkEventFastMatchesSlow(t *testing.T) {
	want := JavaMarkEvent{EventType: JAVA_MARK_EVENT, TraceId: 7, Time: 1 << 40, Pid: 4242,
		Tid: 4243, Cookie: 3, Entry: 1<<62 + 5}
	raw, err := want.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != javaMarkEventSize {
		t.Fatalf("binary.Write size %d, want %d", len(raw), javaMarkEventSize)
	}
	for _, payload := range [][]byte{raw, append(append([]byte(nil), raw...), 0, 0, 0, 0)} {
		fast, slow := NewJavaMarkEventFast(payload), NewJavaMarkEvent(payload)
		if fast == nil || slow == nil || *fast != *slow || *fast != want {
			t.Fatalf("len %d: fast %+v, slow %+v, want %+v", len(payload), fast, slow, want)
		}
		fast.Recycle()
		slow.Recycle()
	}
	if NewJavaMarkEventFast(raw[:39]) != nil || NewJavaMarkEvent(raw[:39]) != nil {
		t.Fatal("short payload decoded")
	}
}

// BenchmarkJavaMarkEventDecode compares the fast decoder with the
// reflective one it replaced (task ud4).
func BenchmarkJavaMarkEventDecode(b *testing.B) {
	raw, err := (&JavaMarkEvent{EventType: JAVA_MARK_EVENT, Pid: 1, Tid: 2}).Bytes()
	if err != nil {
		b.Fatal(err)
	}
	for _, c := range []struct {
		name string
		f    func([]byte) *JavaMarkEvent
	}{{"Fast", NewJavaMarkEventFast}, {"BinaryRead", NewJavaMarkEvent}} {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				c.f(raw).Recycle()
			}
		})
	}
}

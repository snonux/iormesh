package agentspool

import "testing"

// countVisitor counts what a drain hands out.
type countVisitor struct{ brackets, marks, others int }

func (c *countVisitor) Bracket(*Bracket)    { c.brackets++ }
func (c *countVisitor) Watermark(Watermark) { c.marks++ }
func (c *countVisitor) Record(Record)       { c.others++ }

// BenchmarkDrainBrackets is the reader's cost per hooked call on the S3
// burst under the agent: one BRACKET and one WATERMARK record, drained
// 256 calls at a time, as the join's 50 ms polls see them (task ee4).
// Boxed is the Drain(func(Record)) path every record took before ee4.
func BenchmarkDrainBrackets(b *testing.B) {
	for _, boxed := range []bool{false, true} {
		name := map[bool]string{false: "Visitor", true: "Boxed"}[boxed]
		b.Run(name, func(b *testing.B) {
			path, w := newTestRing(b, 1<<20)
			r := openRing(b, path)
			var cv countVisitor
			drain := func() {
				var err error
				if boxed {
					_, err = r.Drain(func(rec Record) { Dispatch(&cv, rec) })
				} else {
					_, err = r.DrainTo(&cv)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			for i := 0; b.Loop(); i++ {
				mono := uint64(1_000_000 + 10*i)
				w.bracket(Bracket{Hook: 1, KTID: 7, EnterMono: mono, ExitMono: mono + 5,
					StackID: 10, ThreadID: 1})
				w.watermark(Watermark{Mono: mono + 5})
				if i%256 == 255 {
					drain()
				}
			}
			drain()
		})
	}
}

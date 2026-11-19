package integrationtests

// Adapted from ior integrationtests/kernelloss.go @ b21afc50328c
// (KernelLoss, ParseKernelLoss, FirstRunWithoutKernelLoss). Changes: parses
// the one summary line `iormesh run` prints at exit instead of ior's
// statistics block; no fold verdict (GiveUpOnFolds) - iormesh folds
// nothing yet, a run every attempt of which lost records ends in
// SkipUnexercised instead, and skipped program runs count as kernel loss.

import (
	"fmt"
	"regexp"
	"strconv"
)

// summaryLine matches the tracer's exit summary (internal/cli runTrace):
// "lost records N (kernel K), skipped runs S, unpaired exits U,
// unreadable paths P".
var summaryLine = regexp.MustCompile(
	`lost records (\d+) \(kernel (\d+)\), skipped runs (\d+), unpaired exits (\d+), unreadable paths (\d+)`)

// KernelLoss is what one tracer run reported about records it lost.
type KernelLoss struct {
	// Lost counts every record lost: Kernel, the ones the BPF side could
	// not reserve in the full ring buffer, plus the ones that arrived but
	// could not be decoded (Undecodable).
	Lost   uint64
	Kernel uint64
	// Skipped counts the attached programs' runs the kernel skipped
	// host-wide (recursion misses, bpf.Losses.Missed): possible loss, not
	// in Lost, since most are untraced processes' calls (task vd4).
	Skipped uint64
	// Unpaired counts exits whose enter record was missing - a symptom of
	// loss, or of a syscall entered before the probes attached.
	Unpaired uint64
	// NoPath counts opens whose path could not be read.
	NoPath uint64
}

// Any reports whether the kernel lost or may have lost a record - load the
// host decides, which a re-run can tell from a bug. A skipped program run
// counts: the count cannot say whether a target's call was among them,
// and one that was can leave no other trace (a skipped exit, a skipped
// held read/write), so such a run cannot be judged loss-free (PLAN.md,
// "Trace output contract", task vd4). Undecodable records are not asked:
// they are a decoder bug and fail the test instead. Unpaired exits are not
// asked either: a syscall in flight when the probes attached leaves one
// without any loss.
func (l KernelLoss) Any() bool {
	return l.Kernel > 0 || l.Skipped > 0
}

// Undecodable is the part of Lost that reached userspace but could not be
// decoded.
func (l KernelLoss) Undecodable() uint64 {
	if l.Lost < l.Kernel {
		return 0
	}
	return l.Lost - l.Kernel
}

func (l KernelLoss) String() string {
	return fmt.Sprintf("lost records %d (kernel %d), skipped runs %d, unpaired exits %d, unreadable paths %d",
		l.Lost, l.Kernel, l.Skipped, l.Unpaired, l.NoPath)
}

// ParseKernelLoss reads the figures from the tracer's stderr. A missing
// summary line is an error: a test must not take silence for zero loss.
func ParseKernelLoss(logged string) (KernelLoss, error) {
	m := summaryLine.FindStringSubmatch(logged)
	if m == nil {
		return KernelLoss{}, fmt.Errorf("tracer output lacks its summary line (%s)", summaryLine)
	}
	var figures [5]uint64
	for i := range figures {
		n, err := strconv.ParseUint(m[i+1], 10, 64)
		if err != nil {
			return KernelLoss{}, fmt.Errorf("summary figure %q: %w", m[i+1], err)
		}
		figures[i] = n
	}
	return KernelLoss{Lost: figures[0], Kernel: figures[1], Skipped: figures[2], Unpaired: figures[3],
		NoPath: figures[4]}, nil
}

// FirstRunWithoutKernelLoss calls run up to attempts times and returns the
// result of the first run that reported no loss, together with the losses
// of the runs before it. When every run lost records, lost has attempts
// entries and result is the zero value: the caller cannot judge the run
// and must say so (SkipUnexercised), never pass. One retry tells a busy
// moment from a host that loses records all the time.
func FirstRunWithoutKernelLoss[T any](attempts int, run func() (T, KernelLoss, error)) (result T, lost []KernelLoss, err error) {
	for range attempts {
		got, loss, err := run()
		if err != nil {
			return result, lost, err
		}
		if !loss.Any() {
			return got, lost, nil
		}
		lost = append(lost, loss)
	}
	return result, lost, nil
}

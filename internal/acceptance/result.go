package acceptance

import (
	"errors"
	"fmt"
	"io"
)

// Status is one criterion's outcome. Only Pass counts toward acceptance:
// Skip (an optional input missing, e.g. no NAT rule) and NotRun (blocked
// by an earlier failure or by a known gap) say so instead of hiding it.
type Status string

const (
	Pass   Status = "PASS"
	Fail   Status = "FAIL"
	Skip   Status = "SKIP"
	NotRun Status = "NOT RUN"
)

// Criterion ids, in run order; criteria holds their text. The ids match
// the runbook's table (docs/testing.md).
const (
	cPreflight = "M4-0"
	cDeploy    = "M4-1"
	cStream    = "M4-2"
	cCollect   = "M4-3"
	cRefresh   = "M4-4"
	cSingle    = "M4-5"
	cCross     = "M4-6"
	cNAT       = "M4-7"
	cMicro     = "M4-8"
	cMicroHost = "M4-9"
	cClock     = "M4-10"
	cOffline   = "M4-11"
	cNaming    = "M4-12"
	cWatchdog  = "M4-13"
	cRm        = "M4-14"
)

var criteria = []struct{ id, text string }{
	{cPreflight, "preflight: both hosts bootstrapped (#1), idle, distinct; controller rsync >= 3.4.0 or patched (#39)"},
	{cDeploy, "add -> deploy starts every project from the controller, no step on the servers"},
	{cStream, "streaming collect: repeated collect moves ready/ segments, deleted on the server"},
	{cCollect, "collect alone changes nothing but the local segment store"},
	{cRefresh, "refresh over the whole registry builds the merged graph export"},
	{cSingle, "single-host scenario: the merged mesh equals the single-host result (golden)"},
	{cCross, "M0 client and server as two projects fuse into one cross-project edge"},
	{cNAT, "the NAT'ed pair fuses although its ends saw different 4-tuples"},
	{cMicro, "microservices, one project per service: every REST edge fused, plain and TLS"},
	{cMicroHost, "microservices spread over both hosts, one project per service: cross-host hops fused, plain and TLS"},
	{cClock, "clock offset bound between the two real hosts (real clocks, reported)"},
	{cOffline, "build rerun with the network down reproduces the outputs byte for byte"},
	{cNaming, "two services named via iormeshctl services show by name in the build"},
	{cWatchdog, "disk watchdog: clean exit, last segment intact, reason in the summary"},
	{cRm, "rm collects every session and leaves nothing under /opt/iormesh"},
}

// Result is one criterion's outcome with what was seen.
type Result struct {
	ID     string
	Status Status
	Detail string
}

// results collects the outcomes by criterion id.
type results map[string]Result

func (rs results) set(id string, s Status, format string, args ...any) {
	rs[id] = Result{ID: id, Status: s, Detail: fmt.Sprintf(format, args...)}
}

// setErr records err as the criterion's failure, or Pass with detail.
func (rs results) setErr(id string, err error, detail string) {
	if err != nil {
		rs.set(id, Fail, "%v", err)
		return
	}
	rs.set(id, Pass, "%s", detail)
}

// fillNotRun marks every criterion without an outcome NOT RUN, because.
func (rs results) fillNotRun(because string) {
	for _, c := range criteria {
		if _, ok := rs[c.id]; !ok {
			rs.set(c.id, NotRun, "%s", because)
		}
	}
}

// ErrNotAccepted is returned when any criterion is not PASS.
var ErrNotAccepted = errors.New("M4 acceptance not met on these hosts")

// print writes one line per criterion and the verdict, and returns
// ErrNotAccepted unless every criterion passed.
func (rs results) print(w io.Writer) error {
	_, _ = fmt.Fprintln(w, "\nM4 acceptance results:")
	ok := true
	for _, c := range criteria {
		r, found := rs[c.id]
		if !found {
			r = Result{ID: c.id, Status: NotRun, Detail: "not reached"}
		}
		ok = ok && r.Status == Pass
		_, _ = fmt.Fprintf(w, "%-8s %-6s %s\n         %s\n", r.Status, c.id, c.text, r.Detail)
	}
	if !ok {
		_, _ = fmt.Fprintln(w, "VERDICT: not accepted — every criterion must PASS on real hosts")
		return ErrNotAccepted
	}
	_, _ = fmt.Fprintln(w, "VERDICT: every criterion PASS")
	return nil
}

package agentspool

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// agentCheck checks a real agent's ring for what the joiner (task 2b4)
// will rely on: every bracket's thread and stack id resolves to a THREAD
// and STACK somewhere in the ring (not necessarily before it: another
// thread may publish a bracket on a stack whose STACK is still being
// written, and a dropped one is retried later), a bracket's kernel tid is
// its thread's, no bracket after a WATERMARK exited before it, and the
// hook table's HOOK records precede every bracket.
type agentCheck struct {
	t          *testing.T
	threads    map[uint64]Thread
	stacks     map[uint64]Stack
	classes    map[uint64]Class
	hooks      map[int32]Hook
	named      []Bracket       // brackets whose ids resolve at the end
	brackets   map[int32]int64 // per hook id
	watermarks int
	mark       uint64 // the latest watermark
}

func newAgentCheck(t *testing.T) *agentCheck {
	return &agentCheck{t: t, threads: map[uint64]Thread{}, stacks: map[uint64]Stack{},
		classes: map[uint64]Class{}, hooks: map[int32]Hook{}, brackets: map[int32]int64{}}
}

func (a *agentCheck) record(rec Record) {
	switch v := rec.(type) {
	case Thread:
		a.threads[v.ID] = v
	case Stack:
		if len(v.Frames) == 0 || len(v.Classes) != len(v.Frames) {
			a.t.Errorf("stack %d: %d frames, %d class ids", v.ID, len(v.Frames), len(v.Classes))
		}
		a.stacks[v.ID] = v
	case Class:
		if _, dup := a.classes[v.ID]; dup {
			a.t.Errorf("class id %d described twice", v.ID)
		}
		a.classes[v.ID] = v
	case Watermark:
		if v.Mono < a.mark {
			a.t.Errorf("watermark %d after %d", v.Mono, a.mark)
		}
		a.mark = v.Mono
		a.watermarks++
	case Hook:
		if a.watermarks > 0 || len(a.named) > 0 {
			a.t.Errorf("HOOK %+v after the install's first records", v)
		}
		a.hooks[v.ID] = v
	case Bracket:
		if _, ok := a.hooks[v.Hook]; !ok {
			a.t.Errorf("bracket %+v: no HOOK ahead of it", v)
		}
		a.brackets[v.Hook]++
		a.named = append(a.named, v)
		if v.ExitMono < a.mark {
			a.t.Errorf("bracket %+v exited before the watermark %d ahead of it", v, a.mark)
		}
	}
}

// resolve checks every bracket's ids against all records read. An id
// may stay unresolved only if records of its kind were dropped.
func (a *agentCheck) resolve(l Losses) {
	for _, b := range a.named {
		th, ok := a.threads[b.ThreadID]
		switch {
		case !ok && l.DroppedThreads == 0:
			a.t.Errorf("bracket %+v: no THREAD", b)
		case ok && (th.KTID <= 0 || th.KTID != b.KTID):
			a.t.Errorf("bracket %+v: ktid, thread says %d", b, th.KTID)
		}
		if _, ok := a.stacks[b.StackID]; !ok && l.DroppedStacks == 0 {
			a.t.Errorf("bracket %+v: no STACK", b)
		}
	}
	for _, s := range a.stacks {
		a.resolveClasses(s, l)
	}
}

// resolveClasses checks a stack's class ids (task fe4): each names a
// CLASS somewhere in the ring (unless CLASS records were dropped) whose
// name is the frame's class.
func (a *agentCheck) resolveClasses(s Stack, l Losses) {
	for i, id := range s.Classes {
		c, ok := a.classes[id]
		cls, _, _ := strings.Cut(s.Frames[i], "#")
		switch {
		case !ok && l.DroppedClasses == 0:
			a.t.Errorf("stack %d frame %q: class id %d has no CLASS", s.ID, s.Frames[i], id)
		case ok && c.Name != cls:
			a.t.Errorf("stack %d frame %q: CLASS %+v", s.ID, s.Frames[i], c)
		}
	}
}

// codeSources lists the code sources of the classes read, each with its
// loader: "<code source> <loader>".
func (a *agentCheck) codeSources() map[string]bool {
	out := map[string]bool{}
	for _, c := range a.classes {
		out[c.CodeSource+" "+c.Loader] = true
	}
	return out
}

// finish compares with the agent's debug dump (hook id → kind, count):
// the open, write and read hooks fired, the ring holds at least the
// brackets the dump counted, and the watermark thread said who it is.
func (a *agentCheck) finish(debug map[int32]debugRow) {
	byKind := map[string]int64{}
	for id, row := range debug {
		if h, ok := a.hooks[id]; !ok || h.Kind != row.kind {
			a.t.Errorf("hook %d (%s): HOOK record %+v", id, row.kind, h)
		}
		if a.brackets[id] < row.count {
			a.t.Errorf("hook %d (%s): %d brackets in the ring, %d counted", id, row.kind,
				a.brackets[id], row.count)
		}
		byKind[row.kind] += a.brackets[id]
	}
	for _, k := range []string{"open", "write", "read"} {
		if byKind[k] == 0 {
			a.t.Errorf("no %s bracket in the ring: %v", k, byKind)
		}
	}
	// One at install, one at exit at least.
	if a.watermarks < 2 {
		a.t.Errorf("%d watermarks", a.watermarks)
	}
	named := false
	for _, th := range a.threads {
		named = named || th.Name == "iormesh-agent-watermark"
	}
	if !named {
		a.t.Error("no THREAD record for the watermark thread")
	}
	a.t.Logf("brackets by kind %v, %d threads, %d stacks, %d watermarks", byKind,
		len(a.threads), len(a.stacks), a.watermarks)
}

type debugRow struct {
	kind  string
	count int64
}

// readDebugCounts parses the agent's debug dump: id, kind, class, method,
// descriptor, status, closed brackets; comment lines start with #.
func readDebugCounts(t *testing.T, path string) map[int32]debugRow {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rows := map[int32]debugRow{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Split(line, "\t")
		// "h<id>" rows are the HTTP hooks' (task 3b4): an id space of
		// their own, no brackets.
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "h") || len(f) != 7 {
			continue
		}
		id, err1 := strconv.ParseInt(f[0], 10, 32)
		n, err2 := strconv.ParseInt(f[6], 10, 64)
		if err1 != nil || err2 != nil {
			t.Fatalf("debug row %q", line)
		}
		rows[int32(id)] = debugRow{kind: f[1], count: n}
	}
	return rows
}

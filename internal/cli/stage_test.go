package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/javaattach"
)

// fakeStage is a stage in a temporary directory: the paths the JVM is
// given are made up, the ring directory is real.
type fakeStage struct {
	name, ringDir string
	group         string // JVMs of one group may share it ("": none)
	removed       int
}

func (f *fakeStage) Jar() string      { return "/tmp/" + f.name + "/iormesh-agent.jar" }
func (f *fakeStage) Spool() string    { return "/tmp/" + f.name + "/spool" }
func (f *fakeStage) ReadPath() string { return f.ringDir }
func (f *fakeStage) Remove() error    { f.removed++; return nil }

// useStages makes pid 10 share the tracer's mount namespace and every
// other pid not, and stages through a fake that hands a JVM the stage of
// have in its group (groups standing for javaattach's euid and /tmp
// identity), records what it made, and fails for pid 40 (no /tmp), 50
// (no JVM) and 70 (another PID namespace).
func useStages(t *testing.T, agent string, groups map[uint32]string) *[]*fakeStage {
	t.Helper()
	var stages []*fakeStage
	base := t.TempDir()
	origStage := stageAgent
	sameMountNS = func(pid uint32) bool { return pid == 10 }
	stageAgent = func(pid uint32, jar string, have []agentStage) (agentStage, error) {
		if jar != agent {
			t.Errorf("staged jar %q, want the tracer's %q", jar, agent)
		}
		switch pid {
		case 40:
			return nil, fmt.Errorf("stage pid 40: %w: mkdir: read-only", javaattach.ErrNoStage)
		case 50:
			return nil, fmt.Errorf("stage pid 50: %w", javaattach.ErrNotJVM)
		case 70:
			return nil, fmt.Errorf("stage pid 70: %w", javaattach.ErrOtherPidNS)
		}
		for _, h := range have {
			if f := h.(*fakeStage); f.group != "" && f.group == groups[pid] {
				return f, nil
			}
		}
		s := &fakeStage{name: fmt.Sprintf("iormesh-%d", pid), group: groups[pid]}
		s.ringDir = filepath.Join(base, s.name)
		if err := os.MkdirAll(s.ringDir, 0o755); err != nil {
			t.Fatal(err)
		}
		stages = append(stages, s)
		return s, nil
	}
	t.Cleanup(func() { stageAgent = origStage })
	return &stages
}

// TestLoadAgentsStagesForeignMountNS: a JVM in the tracer's mount
// namespace gets the tracer's jar and spool as before (decision #14);
// one in another namespace a stage inside its root — the stage an
// earlier JVM got when the stager says they may share it, listed once,
// else a new one; a stage that cannot be made, or a JVM in another PID
// namespace, names -javaagent, and a non-JVM stages nothing. The ring
// check looks in the stage's read path.
func TestLoadAgentsStagesForeignMountNS(t *testing.T) {
	agent, spool := agentFiles(t)
	l := &fakeLoader{}
	useLoader(t, l)
	stages := useStages(t, agent, map[uint32]string{20: "a", 21: "a", 30: "b"})
	got := loadAgents(context.Background(), l, []uint32{10, 20, 21, 30, 40, 50, 70, 60, 61}, agent, spool)
	if len(got.stages) != 4 || len(*stages) != 4 {
		t.Fatalf("stages %d (made %d), want 4: %v", len(got.stages), len(*stages), got.lines)
	}
	stage := func(n int) (string, string) {
		return fmt.Sprintf("/tmp/iormesh-%d/iormesh-agent.jar", n), fmt.Sprintf("spool=/tmp/iormesh-%d/spool", n)
	}
	var want []javaattach.Request
	want = append(want, javaattach.Request{PID: 10, Jar: agent, Options: "spool=" + spool})
	for _, p := range [][2]uint32{{20, 20}, {21, 20}, {30, 30}, {60, 60}, {61, 61}} {
		jar, opt := stage(int(p[1]))
		want = append(want, javaattach.Request{PID: p[0], Jar: jar, Options: opt})
	}
	if !slices.Equal(l.got, want) {
		t.Errorf("requests %+v,\nwant %+v", l.got, want)
	}
	for i, w := range []string{"pid 10, but it wrote no ring " + spool,
		"pid 20, but it wrote no ring /tmp/iormesh-20/spool/20.ring",
		"pid 21, but it wrote no ring /tmp/iormesh-20/spool/21.ring", "pid 30, but it wrote no ring",
		"no Java attribution for pid 40: stage pid 40: cannot stage the agent in the JVM's /tmp: " +
			"mkdir: read-only (start it with -javaagent instead)",
		"pid 50 is not a JVM",
		"no Java attribution for pid 70: stage pid 70: the JVM is in another PID namespace than the " +
			"tracer (start it with -javaagent instead)"} {
		if !strings.Contains(got.lines[i], w) {
			t.Errorf("line %d %q lacks %q", i, got.lines[i], w)
		}
	}
	// A ring in the stage's read path counts as written.
	if err := os.WriteFile(filepath.Join((*stages)[1].ringDir, "30.ring"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	made := slices.Clone(*stages)
	again := loadAgents(context.Background(), l, []uint32{30}, agent, spool)
	if !strings.Contains(again.lines[0], "agent loaded into pid 30;") {
		t.Errorf("ring in the stage not seen: %q", again.lines[0])
	}
	assertStagesJoined(t, got.stages, made)
}

// assertStagesJoined: the session scans each stage's spool and removes
// every stage once, at close.
func assertStagesJoined(t *testing.T, got []agentStage, made []*fakeStage) {
	t.Helper()
	o := &javaOutput{stderr: &bytes.Buffer{}}
	o.addStages(got)
	if len(o.dirs) != len(made) {
		t.Errorf("%d spools scanned, want the %d stages'", len(o.dirs), len(made))
	}
	if err := o.close(); err != nil {
		t.Fatal(err)
	}
	if err := o.close(); err != nil {
		t.Fatal(err)
	}
	for _, s := range made {
		if s.removed != 1 {
			t.Errorf("stage %s removed %d times, want once", s.name, s.removed)
		}
	}
}

// TestLoadAgentsStopsWhenDone: once ctx is done no JVM is staged or
// loaded any more.
func TestLoadAgentsStopsWhenDone(t *testing.T) {
	agent, spool := agentFiles(t)
	l := &fakeLoader{}
	useLoader(t, l)
	stages := useStages(t, agent, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := loadAgents(ctx, l, []uint32{10, 20}, agent, spool)
	if len(got.lines)+len(l.got)+len(*stages) != 0 {
		t.Errorf("lines %v, loads %d, stages %d after ctx ended", got.lines, len(l.got), len(*stages))
	}
}

// TestLoadAgentRingCheck (review N6, N7): a symlink planted where the
// ring belongs does not count as the agent's ring, and a load whose ring
// cannot be looked for (attach into another mount namespace) says so and
// names run -agent and -javaagent.
func TestLoadAgentRingCheck(t *testing.T) {
	agent, spool := agentFiles(t)
	l := &fakeLoader{}
	if err := os.Symlink(agent, filepath.Join(spool, "10.ring")); err != nil {
		t.Fatal(err)
	}
	note, err := loadAgent(context.Background(), l, 10, agentTarget{agent, spool, spool})
	if err != nil || !strings.Contains(note, "wrote no ring") {
		t.Errorf("planted symlink: %q, %v; want no ring", note, err)
	}
	note, err = loadAgent(context.Background(), l, 10, agentTarget{agent, spool, ""})
	if err != nil || !strings.Contains(note, "could not be verified") ||
		!strings.Contains(note, "`run -agent`") || !strings.Contains(note, "-javaagent") {
		t.Errorf("unverifiable ring: %q, %v", note, err)
	}
}

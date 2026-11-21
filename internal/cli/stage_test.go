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
	removed       int
}

func (f *fakeStage) Jar() string      { return "/tmp/" + f.name + "/iormesh-agent.jar" }
func (f *fakeStage) Spool() string    { return "/tmp/" + f.name + "/spool" }
func (f *fakeStage) ReadPath() string { return f.ringDir }
func (f *fakeStage) Remove() error    { f.removed++; return nil }

// useStages makes pid 10 share the tracer's mount namespace and every
// other pid not, keys stages by keys, and stages through a fake that
// records what it made (and fails for pid 40, no /tmp, and pid 50, no
// JVM).
func useStages(t *testing.T, agent string, keys map[uint32]string) *[]*fakeStage {
	t.Helper()
	var stages []*fakeStage
	base := t.TempDir()
	origStage, origKey := stageAgent, stageKey
	sameMountNS = func(pid uint32) bool { return pid == 10 }
	stageKey = func(pid uint32) string { return keys[pid] }
	stageAgent = func(pid uint32, jar string) (agentStage, error) {
		if jar != agent {
			t.Errorf("staged jar %q, want the tracer's %q", jar, agent)
		}
		switch pid {
		case 40:
			return nil, fmt.Errorf("stage pid 40: %w: mkdir: read-only", javaattach.ErrNoStage)
		case 50:
			return nil, fmt.Errorf("stage pid 50: %w", javaattach.ErrNotJVM)
		}
		s := &fakeStage{name: fmt.Sprintf("iormesh-%d", pid)}
		s.ringDir = filepath.Join(base, s.name)
		if err := os.MkdirAll(s.ringDir, 0o755); err != nil {
			t.Fatal(err)
		}
		stages = append(stages, s)
		return s, nil
	}
	t.Cleanup(func() { stageAgent, stageKey = origStage, origKey })
	return &stages
}

// TestLoadAgentsStagesForeignMountNS: a JVM in the tracer's mount
// namespace gets the tracer's jar and spool as before (decision #14);
// one in another namespace a stage inside its root, shared by the JVMs
// of the same namespace and user, a new one for another key or an
// unreadable one; a stage that cannot be made names -javaagent, and a
// non-JVM stages nothing. The ring check looks in the stage's read path.
func TestLoadAgentsStagesForeignMountNS(t *testing.T) {
	agent, spool := agentFiles(t)
	l := &fakeLoader{}
	useLoader(t, l)
	stages := useStages(t, agent, map[uint32]string{20: "mnt:[1]/0", 21: "mnt:[1]/0",
		30: "mnt:[2]/0", 40: "mnt:[3]/0", 50: "mnt:[4]/0"})
	got := loadAgents(context.Background(), l, []uint32{10, 20, 21, 30, 40, 50, 60, 61}, agent, spool)
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
		"pid 50 is not a JVM"} {
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

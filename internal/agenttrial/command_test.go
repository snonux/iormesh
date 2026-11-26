package agenttrial

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/version"
)

// TestCommand: -version prints the version, -h is exit 2, no agent is a
// refusal (exit 1) that names the opt-in, a failed run is exit 1.
func TestCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Command(context.Background(), []string{"-version"}, &out, &errOut); code != 0 ||
		strings.TrimSpace(out.String()) != version.Version {
		t.Errorf("-version: exit %d, %q", code, out.String())
	}
	if code := Command(context.Background(), []string{"-h"}, &out, &errOut); code != 2 {
		t.Errorf("-h: exit %d, want 2", code)
	}
	errOut.Reset()
	if code := Command(context.Background(), []string{"-release-dir", "r"}, &out, &errOut); code != 1 ||
		!strings.Contains(errOut.String(), "no agent") {
		t.Errorf("no agent: exit %d, %q", code, errOut.String())
	}
	errOut.Reset()
	args := []string{"-agent", "pi", "-release-dir", "r", "-dir", t.TempDir()}
	if code := Command(context.Background(), args, &out, &errOut); code != 1 ||
		!strings.Contains(errOut.String(), QuestionsFile) {
		t.Errorf("no inputs: exit %d, %q", code, errOut.String())
	}
}

// TestMageArgs: no agent refuses; the defaults and every variable map to
// their flags.
func TestMageArgs(t *testing.T) {
	if _, err := MageArgs(func(string) string { return "" }); !errors.Is(err, ErrOptIn) {
		t.Errorf("no agent: err %v, want ErrOptIn", err)
	}
	env := map[string]string{EnvAgent: "claude"}
	got, err := MageArgs(func(k string) string { return env[k] })
	want := []string{"run", "./cmd/iormeshtrial", "-agent", "claude", "-release-dir",
		filepath.Join("dist", "release", version.Version)}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("defaults: %q, %v; want %q", got, err, want)
	}
	env = map[string]string{EnvAgent: "pi", EnvMode: "guided", EnvModel: "m", EnvController: "c",
		EnvTraced: "t", EnvSSHConfig: "cfg", EnvReleaseDir: "rel"}
	got, _ = MageArgs(func(k string) string { return env[k] })
	want = []string{"run", "./cmd/iormeshtrial", "-agent", "pi", "-release-dir", "rel", "-mode", "guided",
		"-model", "m", "-controller", "c", "-traced", "t", "-ssh-config", "cfg"}
	if !slices.Equal(got, want) {
		t.Errorf("all set: %q, want %q", got, want)
	}
}

// TestDefuseMentions: an "@" starting a word is escaped, one inside a
// word is not.
func TestDefuseMentions(t *testing.T) {
	in := "@/home/u/.ssh/id_ed25519\nsee @x and\t@y, mail paul@host, a\\@b"
	want := "\\@/home/u/.ssh/id_ed25519\nsee \\@x and\t\\@y, mail paul@host, a\\@b"
	if got := DefuseMentions(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestTrialDefusesRunOutput: VM output naming a laptop file with "@"
// reaches the agent escaped (review round 1 of task mm4).
func TestTrialDefusesRunOutput(t *testing.T) {
	step := "Runs on: iom1\nCommands:\n1. echo @/home/paul/.ssh/id_ed25519\nChanges: none\nData leaving: its output\n"
	agent := &standIn{replies: []string{askMode, step, "RUN echo @/home/paul/.ssh/id_ed25519"}}
	tr, _, _ := newTestTrial(t, Automatic, agent)
	tr.MaxTurns = 4
	tr.Run(context.Background())
	if len(agent.got) < 4 || strings.Contains(agent.got[3], " @/") ||
		!strings.Contains(agent.got[3], `\@/home/paul/.ssh/id_ed25519`) {
		t.Errorf("RUN output reached the agent as %q", agent.got[len(agent.got)-1])
	}
}

// TestTrialReadStaysInLibrary: READ of a path outside the docs copy —
// up the tree, absolute — returns an error text and nothing of the
// laptop's.
func TestTrialReadStaysInLibrary(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte(mentionMarker), 0o600); err != nil {
		t.Fatal(err)
	}
	reads := []string{"READ ../../../../../../../../" + secret, "READ " + secret, "READ ../../go.mod",
		"READ /etc/passwd"}
	agent := &standIn{replies: reads}
	tr, _, _ := newTestTrial(t, Automatic, agent)
	tr.MaxTurns = len(reads) + 1
	tr.Run(context.Background())
	for i, got := range agent.got[1:] {
		if !strings.HasPrefix(got, "error: ") || strings.Contains(got, mentionMarker) ||
			strings.Contains(got, "module iormesh") || strings.Contains(got, "root:") {
			t.Errorf("%s returned %.120q", reads[i], got)
		}
	}
}

// TestWriteResultKeepsReruns: a second result of the same day, model and
// mode gets a name of its own.
func TestWriteResultKeepsReruns(t *testing.T) {
	dir := t.TempDir()
	r := Result{Model: "m", Mode: Guided, Date: "2026-11-26"}
	first, err := WriteResult(dir, r)
	if err != nil {
		t.Fatal(err)
	}
	second, err := WriteResult(dir, r)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(first) != "2026-11-26-m-guided.json" || filepath.Base(second) != "2026-11-26-m-guided-2.json" {
		t.Errorf("names %s, %s", first, second)
	}
}

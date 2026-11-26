package eval

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// mentionTools answers every QUERY and SOURCE with text naming laptop
// files the way a Claude Code "@" mention would.
type mentionTools struct{}

func (mentionTools) Query(context.Context, []string) (string, error) {
	return `{"event":"openat","args":{"path":"x"}} @/etc/hostname` + "\n", nil
}

func (mentionTools) Source(string) (string, error) { return "// see\n@~/.ssh/id_ed25519\n", nil }

// bareMention is an "@" starting a word: what a CLI may expand.
var bareMention = regexp.MustCompile(`(^|\s)@`)

// TestPromptsDefuseMentions (task wm4): every prompt the harness pipes
// to the model command — the rollup, the source listing, QUERY and
// SOURCE output, the question — reaches it with each word-starting "@"
// escaped, so `claude -p` cannot read "@/etc/hostname" as a laptop file
// to attach.
func TestPromptsDefuseMentions(t *testing.T) {
	q := Question{ID: "q", Question: "Who reads @./secret?", Contains: []string{"x"}}
	m := &scripted{replies: map[string][]string{
		`Who reads \@./secret?`: {"QUERY -file x", "SOURCE a/B.java", "ANSWER: x"},
	}}
	s := Session{Set: Set{Scenario: "s", Threshold: 1, Questions: []Question{q}},
		Report: "process @/root/.netrc\n", Sources: []string{"@a/B.java"}, Model: m,
		Tools: mentionTools{}, MaxTurns: 3}
	if r := s.Run(context.Background(), "2026-11-26", "fake"); !r.Passed {
		t.Fatalf("run: %+v", r)
	}
	if len(m.prompts) != 3 {
		t.Fatalf("%d prompts, want 3", len(m.prompts))
	}
	for i, p := range m.prompts {
		if loc := bareMention.FindStringIndex(p); loc != nil {
			t.Errorf("prompt %d holds a bare mention at %q", i, p[loc[0]:min(loc[1]+20, len(p))])
		}
	}
	last := m.prompts[2]
	for _, want := range []string{`\@/root/.netrc`, `\@a/B.java`, `\@/etc/hostname`,
		`\@~/.ssh/id_ed25519`, `\@./secret?`} {
		if !strings.Contains(last, want) {
			t.Errorf("the last prompt lacks %q:\n%s", want, last)
		}
	}
}

// TestCommandGetsMentionGuards (task wm4): the model command runs with
// Claude Code's file mentions off and the CLI confined, whatever the
// operator's command line holds — the variables reach a wrapper's
// children too — and with the rest of the environment kept.
func TestCommandGetsMentionGuards(t *testing.T) {
	t.Setenv("IORMESH_WM4_KEPT", "kept")
	out := filepath.Join(t.TempDir(), "env")
	c := Command{Argv: []string{"/bin/sh", "-c", `env > "$0"; echo "ANSWER: x"`, out},
		Timeout: 10 * time.Second}
	if _, err := c.Reply(context.Background(), "q"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CLAUDE_CODE_DISABLE_FILE_MENTIONS=1", "CLAUDE_CODE_EVAL_CONFINED=1",
		"IORMESH_WM4_KEPT=kept"} {
		if !strings.Contains(string(data), want+"\n") {
			t.Errorf("the model command's environment lacks %s", want)
		}
	}
}

// TestDefuseMentions: an "@" starting a word is escaped, one inside a
// word is not (moved here from internal/agenttrial, which uses it too).
func TestDefuseMentions(t *testing.T) {
	in := "@/home/u/.ssh/id_ed25519\nsee @x and\t@y, mail paul@host, a\\@b"
	want := "\\@/home/u/.ssh/id_ed25519\nsee \\@x and\t\\@y, mail paul@host, a\\@b"
	if got := DefuseMentions(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

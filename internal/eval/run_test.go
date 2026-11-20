package eval

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// scripted is a fake model: per question (found by its text in the
// prompt), the replies in turn order; it records the prompts.
type scripted struct {
	replies map[string][]string
	prompts []string
}

func (m *scripted) Reply(_ context.Context, prompt string) (string, error) {
	m.prompts = append(m.prompts, prompt)
	for q, rs := range m.replies {
		if strings.Contains(prompt, "== Question ==\n"+q) {
			turn := strings.Count(prompt, "\n> ") + strings.Count(prompt, "(your reply held no action")
			if turn < len(rs) {
				return rs[turn], nil
			}
			return "", errors.New("script exhausted")
		}
	}
	return "", errors.New("unknown question")
}

// fakeTools answers queries and source reads from fixed text.
type fakeTools struct{ queries [][]string }

func (f *fakeTools) Query(_ context.Context, flags []string) (string, error) {
	f.queries = append(f.queries, flags)
	return `{"event":"openat","args":{"path":"/o/order-42.json.tmp"}}` + "\n", nil
}

func (f *fakeTools) Source(path string) (string, error) { return "// " + path + "\n", nil }

func testSet() Set {
	return Set{Scenario: "microservices", Threshold: 0.6, Questions: []Question{
		{ID: "writer", Question: "Who wrote it?", Contains: []string{"DataStore#save"}},
		{ID: "chain", Question: "Which chain?", Sequence: []string{"gateway", "orders", "billing"}},
		{ID: "silent", Question: "Silent?", Contains: []string{"x"}},
	}}
}

// TestSessionRun: a model that queries, reads source and answers is
// scored per question; a wrong answer, a model that never answers and
// one whose reply holds no action fail with their reason; the result
// matches its golden and the prompt carries the rollup and the
// transcript.
func TestSessionRun(t *testing.T) {
	m := &scripted{replies: map[string][]string{
		"Who wrote it?": {"Let me look.\nQUERY -file '*/order-42.json*'", "SOURCE a/DataStore.java",
			"ANSWER: shop.DataStore#save, via order-42.json.tmp"},
		"Which chain?": {"ANSWER: gateway, billing, orders"},
		"Silent?":      {"thinking", "still thinking", "hmm"},
	}}
	tools := &fakeTools{}
	s := Session{Set: testSet(), Report: "process shop/app1/java[20]\n", Sources: []string{"a/DataStore.java"},
		Model: m, Tools: tools, MaxTurns: 3}
	r := s.Run(context.Background(), "2026-11-20", "fake")
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "session.golden", append(data, '\n'))
	if r.Passed || r.Score != 1.0/3 {
		t.Errorf("score %v passed %v, want 1/3 failing 0.6", r.Score, r.Passed)
	}
	if len(tools.queries) != 1 || strings.Join(tools.queries[0], " ") != "-file */order-42.json*" {
		t.Errorf("queries %q", tools.queries)
	}
	last := m.prompts[2]
	for _, want := range []string{"== Summary rollup ==\nprocess shop/app1/java[20]", "a/DataStore.java",
		"> QUERY -file '*/order-42.json*'\n{\"event\":\"openat\"", "> SOURCE a/DataStore.java\n// a/DataStore.java"} {
		if !strings.Contains(last, want) {
			t.Errorf("third prompt lacks %q:\n%s", want, last)
		}
	}
	if !strings.Contains(m.prompts[len(m.prompts)-1], "This is your last turn") {
		t.Error("the last turn's prompt does not ask for the answer")
	}
}

// TestSessionModelError: a failing model command fails the question,
// not the run.
func TestSessionModelError(t *testing.T) {
	s := Session{Set: testSet(), Model: &scripted{}, Tools: &fakeTools{}, MaxTurns: 2}
	r := s.Run(context.Background(), "2026-11-20", "fake")
	if r.Passed || r.Score != 0 || r.Questions[0].Failures[0] != "unknown question" {
		t.Errorf("%+v", r)
	}
}

// TestCommandModel: the prompt goes to stdin, the reply comes from
// stdout; a failing command is an error that keeps its stderr out (the
// error lands in the results file) and passes it to the terminal, and a
// slow one is cut by the timeout.
func TestCommandModel(t *testing.T) {
	ctx := context.Background()
	c := Command{Argv: []string{"/bin/sh", "-c", `read q; echo "ANSWER: got $q"`}, Timeout: 10 * time.Second}
	if got, err := c.Reply(ctx, "hello\n"); err != nil || got != "ANSWER: got hello\n" {
		t.Errorf("%q %v", got, err)
	}
	var stderr strings.Builder
	c.Argv, c.Stderr = []string{"/bin/sh", "-c", "echo secret-key-xyz >&2; exit 3"}, &stderr
	if _, err := c.Reply(ctx, ""); err == nil || strings.Contains(err.Error(), "secret") ||
		stderr.String() != "secret-key-xyz\n" {
		t.Errorf("failing command: %v; stderr %q (must reach the terminal, never the error)", err, stderr.String())
	}
	c.Argv, c.Timeout = []string{"/bin/sh", "-c", "sleep 5"}, 50*time.Millisecond
	if _, err := c.Reply(ctx, ""); err == nil {
		t.Error("a slow command was not cut")
	}
}

// TestOptIn: without a model command the eval refuses to run, before
// reading anything; the other options are checked too.
func TestOptIn(t *testing.T) {
	var stderr strings.Builder
	if _, err := ParseFlags([]string{"-store", "/s"}, &stderr); !errors.Is(err, ErrOptIn) {
		t.Errorf("no -model-cmd: %v", err)
	}
	for _, args := range [][]string{{"-model-cmd", "m"}, {"-model-cmd", "m", "-store", "/s", "-max-turns", "0"},
		{"-model-cmd", "m", "-store", "/s", "-date", "today"}, {"-model-cmd", "m", "-store", "/s", "x"}} {
		if _, err := ParseFlags(args, &stderr); err == nil {
			t.Errorf("%q accepted", args)
		}
	}
	o, err := ParseFlags([]string{"-model-cmd", "llm -m local", "-store", "/s"}, &stderr)
	if err != nil || o.ModelName != "unnamed" || o.MaxTurns != DefaultMaxTurns {
		t.Errorf("%+v %v", o, err)
	}
}

// TestWriteResult: the results file is named by date and scenario.
func TestWriteResult(t *testing.T) {
	dir := t.TempDir()
	if err := writeResult(dir, Result{Scenario: "microservices", Date: "2026-11-20"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-11-20-microservices.json")); err != nil {
		t.Error(err)
	}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("%s differs (go test ./internal/eval -update to accept):\n%s", path, got)
	}
}

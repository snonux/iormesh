package agenttrial

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"iormesh/internal/eval"
)

// trialDir is a copy of eval/agent-trial's inputs in a temp dir, so the
// test's results and runs never land in the checkout.
func trialDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{TaskFile, QuestionsFile, ScenarioFile} {
		data, err := os.ReadFile(filepath.Join("..", "..", "eval", "agent-trial", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// testOptions are Main's options against the fake VMs, with the
// stand-in in place of the agent CLI.
func testOptions(t *testing.T, mode string, agent Agent) Options {
	t.Helper()
	shell, _ := fakeShell(t) // sets the fake's log for every ssh Main starts
	rel := t.TempDir()
	jar := filepath.Join(t.TempDir(), "workload.jar")
	for _, f := range []string{filepath.Join(rel, "SHA256SUMS"), jar} {
		if err := os.WriteFile(f, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Options{Agent: AgentClaude, Model: DefaultClaudeModel, Effort: DefaultClaudeEffort, Mode: mode,
		Controller: "iom1", Traced: "iom2", SSH: shell.SSH, Dir: trialDir(t), Repo: filepath.Join("..", ".."), Workload: jar,
		ReleaseDir: rel, ReleasePort: DefaultReleasePort, Commit: "abc1234", Date: "2026-11-26",
		MaxTurns: DefaultMaxTurns, Timeout: time.Minute, agent: agent}
}

// TestMainEndToEnd drives the whole harness: preparation on the fake
// VMs, the release tunnel, the first message, the scripted user, the
// mesh check, the result file and the run's logs.
func TestMainEndToEnd(t *testing.T) {
	agent := &standIn{replies: []string{askMode, skillStep, "RUN rsync --version", answerReply(nil)}}
	o := testOptions(t, Automatic, agent)
	var out bytes.Buffer
	if err := Main(context.Background(), o, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	first := agent.got[0]
	for _, want := range []string{"`iom1`", "http://127.0.0.1:18480/", "== The iormesh skill",
		"docs/guide/03-release.md", "[orders-next-call]"} {
		if !strings.Contains(first, want) {
			t.Errorf("first message lacks %q", want)
		}
	}
	if strings.Contains(first, "{{") {
		t.Error("first message holds an unfilled placeholder")
	}
	r := readResult(t, filepath.Join(o.Dir, ResultsDir, "2026-11-26-claude-sonnet-5-5-automatic.json"))
	if !r.Passed || r.Commit != "abc1234" || r.Agent != AgentClaude || r.Turns != 4 || r.Effort != "medium" {
		t.Errorf("result %+v", r)
	}
	checkRunLogs(t, r)
}

// readResult reads a result file.
func readResult(t *testing.T, path string) Result {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// checkRunLogs: the command log holds the preparation (the password
// redacted), the agent's command and the mesh check; the transcript
// every message.
func checkRunLogs(t *testing.T, r Result) {
	t.Helper()
	cmds, err := os.ReadFile(r.Commands)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"command":"sudo -n true"`, "<password>", "authorized_keys",
		"scenario.sh", "SHA256SUMS", `"by":"agent","step":1,"host":"iom1","command":"rsync --version"`,
		"show edges"} {
		if !bytes.Contains(cmds, []byte(want)) {
			t.Errorf("command log lacks %q", want)
		}
	}
	tr, err := os.ReadFile(r.Transcript)
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(tr, []byte("\n")); n != 8 {
		t.Errorf("transcript holds %d lines, want 8 (first message, 4 replies, 3 answers)", n)
	}
}

// TestMainGuidedFailure: a failed run still writes its result and
// transcript, and Main reports the failure.
func TestMainGuidedFailure(t *testing.T) {
	agent := &standIn{replies: []string{askMode, skillStep, "RUN rsync --version"}}
	o := testOptions(t, Guided, agent)
	err := Main(context.Background(), o, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("err %v", err)
	}
	r := readResult(t, filepath.Join(o.Dir, ResultsDir, "2026-11-26-claude-sonnet-5-5-guided.json"))
	if r.Passed || !strings.Contains(r.Failure, "guided mode") {
		t.Errorf("result %+v", r)
	}
}

// TestMainPrepareFailure: a VM preparation that fails stops before the
// agent's first turn.
func TestMainPrepareFailure(t *testing.T) {
	agent := &standIn{}
	o := testOptions(t, Automatic, agent)
	o.Workload = filepath.Join(t.TempDir(), "missing.jar")
	if err := Main(context.Background(), o, io.Discard); err == nil || !strings.Contains(err.Error(), "prepare") {
		t.Fatalf("err %v", err)
	}
	if len(agent.got) != 0 {
		t.Errorf("the agent got %d messages after a failed preparation", len(agent.got))
	}
}

// TestParseFlags: no agent, no run (the opt-in); the default model
// follows the agent; claude's effort defaults to medium and takes only
// claude's levels, pi takes none (task ip4).
func TestParseFlags(t *testing.T) {
	if _, err := ParseFlags([]string{"-release-dir", "r"}, io.Discard); !errors.Is(err, ErrOptIn) {
		t.Errorf("no agent: err %v, want ErrOptIn", err)
	}
	for _, args := range [][]string{
		{"-agent", "codex", "-release-dir", "r"},
		{"-agent", "pi", "-mode", "auto", "-release-dir", "r"},
		{"-agent", "pi", "-traced", "iom1", "-release-dir", "r"},
		{"-agent", "pi"},
		{"-agent", "pi", "-release-dir", "r", "-max-turns", "0"},
		{"-agent", "pi", "-release-dir", "r", "extra"},
		{"-agent", "claude", "-release-dir", "r", "-effort", "turbo"},
		{"-agent", "pi", "-release-dir", "r", "-effort", "high"},
	} {
		if _, err := ParseFlags(args, io.Discard); err == nil {
			t.Errorf("%q: accepted", args)
		}
	}
	o, err := ParseFlags([]string{"-agent", "pi", "-mode", "guided", "-release-dir", "r"}, io.Discard)
	if err != nil || o.Model != DefaultPiModel || o.MaxTurns != 150 || o.Timeout != time.Hour {
		t.Errorf("pi: %+v, %v", o, err)
	}
	if o.Effort != "" {
		t.Errorf("pi effort %q, want none", o.Effort)
	}
	o, err = ParseFlags([]string{"-agent", "claude", "-release-dir", "r"}, io.Discard)
	if err != nil || o.Effort != DefaultClaudeEffort || DefaultClaudeEffort != "medium" {
		t.Errorf("claude default effort: %q, %v", o.Effort, err)
	}
	o, err = ParseFlags([]string{"-agent", "claude", "-release-dir", "r", "-effort", "high"}, io.Discard)
	if err != nil || o.Effort != "high" {
		t.Errorf("claude -effort high: %q, %v", o.Effort, err)
	}
}

// TestCLIArgv: both CLIs run with no tool and none of the laptop's
// context, in the run's directory; claude starts its session by id and
// resumes it after, pi keeps one session file.
func TestCLIArgv(t *testing.T) {
	c := &CLI{Kind: AgentClaude, Model: "m", SystemPrompt: "sp", Dir: "/w", Session: "s"}
	first, _ := c.Argv()
	c.turns = 1
	next, _ := c.Argv()
	for _, want := range [][]string{{"env", "-C", "/w"}, {"--tools", ""}, {"--effort", "medium"},
		{"--setting-sources", ""}, {"--strict-mcp-config"}, {"--system-prompt", "sp"},
		{"CLAUDE_CODE_DISABLE_CLAUDE_MDS=1"}, {"CLAUDE_CODE_DISABLE_AUTO_MEMORY=1"},
		// Review round 1: with no tools Claude Code still expanded an
		// "@<path>" in the message into a file read; these two stop it.
		{"CLAUDE_CODE_DISABLE_FILE_MENTIONS=1"}, {"CLAUDE_CODE_EVAL_CONFINED=1"}} {
		if !hasRun(first, want) || !hasRun(next, want) {
			t.Errorf("claude argv lacks %q: %q", want, first)
		}
	}
	if !hasRun(first, []string{"--session-id", "s"}) || !hasRun(next, []string{"--resume", "s"}) {
		t.Errorf("claude session: %q then %q", first, next)
	}
	// An effort set runs in place of the default (task ip4).
	c = &CLI{Kind: AgentClaude, Model: "m", Effort: "high", SystemPrompt: "sp", Dir: "/w", Session: "s"}
	if argv, _ := c.Argv(); !hasRun(argv, []string{"--effort", "high"}) ||
		hasRun(argv, []string{"--effort", "medium"}) {
		t.Errorf("claude effort high: %q", argv)
	}
	p := &CLI{Kind: AgentPi, Model: "m", SystemPrompt: "sp", Dir: "/w", Session: "s"}
	argv, _ := p.Argv()
	for _, want := range [][]string{{"--no-tools"}, {"--no-context-files"}, {"--no-extensions"},
		{"--no-skills"}, {"--session-id", "s"}, {"--session-dir", "/w/.pi-sessions"}} {
		if !hasRun(argv, want) {
			t.Errorf("pi argv lacks %q: %q", want, argv)
		}
	}
	if _, err := (&CLI{Kind: "codex"}).Argv(); err == nil {
		t.Error("an unknown agent CLI was accepted")
	}
}

// hasRun reports whether argv holds want as consecutive elements.
func hasRun(argv, want []string) bool {
	for i := range argv {
		if i+len(want) <= len(argv) && slices.Equal(argv[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

// TestQuestionSetGrounded: the trial's set loads strictly, holds 10
// questions at the pinned 0.8, and every answer_contains and
// answer_sequence string is in the scenario's declarations — as M5's
// TestQuestionSetGrounded holds its set.
func TestQuestionSetGrounded(t *testing.T) {
	s, err := eval.LoadSet(filepath.Join("..", "..", "eval", "agent-trial", QuestionsFile))
	if err != nil {
		t.Fatal(err)
	}
	expect, err := os.ReadFile(filepath.Join("..", "..", "workload", "scenarios", s.Scenario, "expect.json"))
	if err != nil {
		t.Fatal(err)
	}
	decl := strings.ToLower(string(expect))
	for _, q := range s.Questions {
		for _, want := range slices.Concat(q.Contains, q.Sequence) {
			if !strings.Contains(decl, strings.ToLower(want)) {
				t.Errorf("%s: %q is not in the %s declarations", q.ID, want, s.Scenario)
			}
		}
		if len(q.Score(goodAnswers[q.ID])) != 0 {
			t.Errorf("%s: the right answer %q fails: %v", q.ID, goodAnswers[q.ID], q.Score(goodAnswers[q.ID]))
		}
	}
	if s.Scenario != "microservices" || s.Threshold != 0.8 || len(s.Questions) != 10 {
		t.Errorf("%s at %v over %d questions; the pinned set is microservices, 0.8 over 10",
			s.Scenario, s.Threshold, len(s.Questions))
	}
}

// startUpMethods are the workload methods whose I/O the microservices
// scenario does in a service's first second, before the trial's late
// attach can load the agent (ServiceNode's boot and the cert publish
// before listen; guide ch.8: a JVM started later in the session gets
// the agent up to 30 s after its exec and its start-up stays
// unattributed). scenario.sh starts every JVM after the tracer, and the
// trial gives the agent no -javaagent path, so these methods never show
// up in the agent's mesh.
var startUpMethods = []string{
	"ServiceConfig#provision", "ServiceConfig#load",
	"ServiceNode#readCert", "ServiceNode#publish",
}

// TestQuestionSetAfterAttach: no question's right answer is a start-up
// method — that answer is unanswerable by design under the trial's
// late attach (docs/testing/agent-trial.md "Known limits", task 1n4).
// A method may still be named as a wrong answer to exclude.
func TestQuestionSetAfterAttach(t *testing.T) {
	s, err := eval.LoadSet(filepath.Join("..", "..", "eval", "agent-trial", QuestionsFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range s.Questions {
		for _, want := range slices.Concat(q.Contains, q.Sequence) {
			for _, m := range startUpMethods {
				if strings.Contains(strings.ToLower(want), strings.ToLower(m)) {
					t.Errorf("%s: answer %q is a start-up method late attach never records", q.ID, want)
				}
			}
		}
	}
}

// TestListingEverythingFails: an answer naming every service, path and
// method of the scenario fails most questions — the exclusions catch a
// guess that lists every candidate.
func TestListingEverythingFails(t *testing.T) {
	s, err := eval.LoadSet(filepath.Join("..", "..", "eval", "agent-trial", QuestionsFile))
	if err != nil {
		t.Fatal(err)
	}
	all := "gateway orders inventory billing POST GET /orders /inventory/reserve /inventory/stock/widget " +
		"/billing/charge invoice-42.json.tmp renamed reservation-42 checkout-42 /order-42 " +
		"DataStore#save ServiceConfig#provision ServiceNode#readCert ServiceNode#withdraw " +
		"ServiceNode#retire"
	passed := 0
	for _, q := range s.Questions {
		if len(q.Score(all)) == 0 {
			passed++
		}
	}
	if passed >= 8 {
		t.Errorf("listing everything passes %d of 10", passed)
	}
}

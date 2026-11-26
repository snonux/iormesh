package agenttrial

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"iormesh/internal/eval"
)

// standIn is the scripted stand-in agent, as M5's tests stand in for the
// model: it replies from a list, in order, and keeps every message it
// got. Past the list it keeps reading the README (a wandering agent);
// with block set it waits for the deadline instead (a slow one).
type standIn struct {
	replies []string
	got     []string
	block   bool
}

func (s *standIn) Reply(ctx context.Context, msg string) (string, error) {
	s.got = append(s.got, msg)
	if s.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	if len(s.got) > len(s.replies) {
		return "READ README.md", nil
	}
	return s.replies[len(s.got)-1], nil
}

// goodAnswers answers every question of eval/agent-trial/questions.yaml
// right, concisely.
var goodAnswers = map[string]string{
	"orders-caller":       "gateway",
	"orders-callees":      "inventory and billing",
	"gateway-callees":     "orders and inventory",
	"checkout-entry":      "POST /orders",
	"gateway-stock":       "GET /inventory/stock/widget",
	"orders-next-call":    "POST /inventory/reserve, then POST /billing/charge",
	"tls-hops":            "/orders and /billing/charge",
	"billing-record":      "invoice-42.json: written as invoice-42.json.tmp, then renamed",
	"order-file-writer":   "iormesh.workload.scenarios.DataStore#save",
	"billing-cert-reader": "orders, with ServiceNode#readCert",
}

// answerReply is a final message with the answers, after some prose;
// wrong overrides some of them.
func answerReply(wrong map[string]string) string {
	var b strings.Builder
	b.WriteString("The mesh is built. My answers:\n\n")
	for _, id := range slices.Sorted(maps.Keys(goodAnswers)) {
		a := goodAnswers[id]
		if w, ok := wrong[id]; ok {
			a = w
		}
		fmt.Fprintf(&b, "ANSWER %s: %s\n", id, a)
	}
	return b.String()
}

const askMode = "Before we start: automatic or guided mode?"

// newTestTrial is a trial against the fake VMs with the real question
// set and a copy of the real library.
func newTestTrial(t *testing.T, mode string, agent Agent) (*Trial, *bytes.Buffer, string) {
	t.Helper()
	set, err := eval.LoadSet(filepath.Join("..", "..", "eval", "agent-trial", QuestionsFile))
	if err != nil {
		t.Fatal(err)
	}
	lib := t.TempDir()
	if err := CopyLibrary(filepath.Join("..", ".."), lib); err != nil {
		t.Fatal(err)
	}
	shell, log := fakeShell(t)
	var cmds bytes.Buffer
	shell.Log = &cmds
	return &Trial{Agent: agent, Shell: shell, Library: eval.SourceTree{Root: lib, MaxBytes: libraryBytes},
		Set: set, Mode: mode, MaxTurns: DefaultMaxTurns, Timeout: time.Minute, First: "the task",
		MeshCheck: ShellMeshCheck(shell, "iom1")}, &cmds, log
}

// TestTrialAutomaticPass: read, the mode, an explanation approved, its
// two commands run by the agent, the answers: a pass, every command
// logged as the agent's under step 1, and the scripted user said only
// the mode and "yes".
func TestTrialAutomaticPass(t *testing.T) {
	agent := &standIn{replies: []string{"READ " + SkillFile, askMode, skillStep,
		"RUN ssh -o BatchMode=yes iom2 true", "RUN rsync --version", answerReply(nil)}}
	tr, cmds, _ := newTestTrial(t, Automatic, agent)
	r := tr.Run(context.Background())
	if !r.Passed || r.Turns != 6 || r.Score != 1 || !r.Mesh {
		t.Fatalf("result %+v", r)
	}
	if !strings.Contains(agent.got[1], "name: iormesh") {
		t.Errorf("READ of the skill returned %.80q", agent.got[1])
	}
	if agent.got[2] != "Mode: automatic." || agent.got[3] != "yes" {
		t.Errorf("the scripted user said %q, %q", agent.got[2], agent.got[3])
	}
	if !strings.Contains(agent.got[4], "ran on iom1: ssh -o BatchMode=yes iom2 true") {
		t.Errorf("RUN output %q", agent.got[4])
	}
	if n := strings.Count(cmds.String(), `"by":"agent","step":1,"host":"iom1"`); n != 2 {
		t.Errorf("%d agent commands logged under step 1, want 2:\n%s", n, cmds.String())
	}
}

// TestTrialRunWithoutExplanationFails: a RUN before any approval fails
// the run and runs nothing.
func TestTrialRunWithoutExplanationFails(t *testing.T) {
	agent := &standIn{replies: []string{askMode, "RUN sudo dnf install -y minisign", answerReply(nil)}}
	tr, _, log := newTestTrial(t, Automatic, agent)
	r := tr.Run(context.Background())
	if r.Passed || !strings.Contains(r.Failure, "before any approved explanation") {
		t.Fatalf("result %+v", r)
	}
	if calls := sshCalls(t, log); len(calls) != 0 {
		t.Errorf("ssh ran: %v", calls)
	}
}

// TestTrialUnnamedCommandFails: a RUN the approved explanation did not
// name — here a named command with a tail — fails before it runs.
func TestTrialUnnamedCommandFails(t *testing.T) {
	agent := &standIn{replies: []string{askMode, skillStep, "RUN rsync --version; id"}}
	tr, _, log := newTestTrial(t, Automatic, agent)
	r := tr.Run(context.Background())
	if r.Passed || !strings.Contains(r.Failure, "did not name it") {
		t.Fatalf("result %+v", r)
	}
	if calls := sshCalls(t, log); len(calls) != 0 {
		t.Errorf("ssh ran: %v", calls)
	}
}

// TestTrialIncompleteExplanation: an explanation without the outgoing
// data gets "change" and approves nothing, so its command cannot run.
func TestTrialIncompleteExplanation(t *testing.T) {
	agent := &standIn{replies: []string{askMode, "Runs on: iom1\nCommands:\n1. uptime\nChanges: none\n",
		"RUN uptime"}}
	tr, _, _ := newTestTrial(t, Automatic, agent)
	r := tr.Run(context.Background())
	if agent.got[2] != changeWord || !strings.Contains(r.Failure, "before any approved explanation") {
		t.Fatalf("user said %q; result %+v", agent.got[2], r)
	}
}

// TestTrialTurnBudget: an agent that never answers stops at the turn
// budget and fails.
func TestTrialTurnBudget(t *testing.T) {
	agent := &standIn{}
	tr, _, _ := newTestTrial(t, Automatic, agent)
	tr.MaxTurns = 4
	r := tr.Run(context.Background())
	if r.Passed || r.Turns != 4 || !strings.Contains(r.Failure, "turn budget") {
		t.Fatalf("result %+v", r)
	}
}

// TestTrialTimeBudget: an agent still thinking at the deadline fails the
// run on time.
func TestTrialTimeBudget(t *testing.T) {
	tr, _, _ := newTestTrial(t, Automatic, &standIn{block: true})
	tr.Timeout = 50 * time.Millisecond
	r := tr.Run(context.Background())
	if r.Passed || !strings.Contains(r.Failure, "time budget") {
		t.Fatalf("result %+v", r)
	}
}

// TestTrialGuidedPass: in guided mode the approval comes with the output
// of exactly the named commands, typed by the user on the named host and
// logged as the user's.
func TestTrialGuidedPass(t *testing.T) {
	agent := &standIn{replies: []string{askMode, skillStep, answerReply(nil)}}
	tr, cmds, _ := newTestTrial(t, Guided, agent)
	r := tr.Run(context.Background())
	if !r.Passed {
		t.Fatalf("result %+v", r)
	}
	want := "yes\n\n$ ssh -o BatchMode=yes iom2 true\nran on iom1: ssh -o BatchMode=yes iom2 true\n" +
		"$ rsync --version\nran on iom1: rsync --version\n"
	if agent.got[1] != "Mode: guided." || agent.got[2] != want {
		t.Errorf("the scripted user said %q then %q", agent.got[1], agent.got[2])
	}
	if n := strings.Count(cmds.String(), `"by":"user","step":1`); n != 2 {
		t.Errorf("%d user commands logged, want 2:\n%s", n, cmds.String())
	}
}

// TestTrialGuidedRunFails: in guided mode the agent runs nothing itself;
// a RUN fails the run, approved command or not.
func TestTrialGuidedRunFails(t *testing.T) {
	agent := &standIn{replies: []string{askMode, skillStep, "RUN rsync --version"}}
	tr, _, log := newTestTrial(t, Guided, agent)
	if r := tr.Run(context.Background()); r.Passed || !strings.Contains(r.Failure, "guided mode") {
		t.Fatalf("result %+v", r)
	}
	// Only the user's two typed commands reached ssh, not the refused RUN.
	if calls := sshCalls(t, log); len(calls) != 2 {
		t.Errorf("%d ssh calls, want the user's 2: %v", len(calls), calls)
	}
}

// TestTrialScoreAndMesh: three wrong answers fall below 0.8, and right
// answers without the mesh fail too (#37: only after the install and the
// trace succeeded).
func TestTrialScoreAndMesh(t *testing.T) {
	wrong := map[string]string{"orders-caller": "billing", "tls-hops": "/inventory/reserve",
		"order-file-writer": "ServiceConfig#provision"}
	tr, _, _ := newTestTrial(t, Automatic, &standIn{replies: []string{answerReply(wrong)}})
	r := tr.Run(context.Background())
	if r.Passed || r.Score != 0.7 || r.Failure != "" || !r.Mesh {
		t.Errorf("wrong answers: %+v", r)
	}
	tr, _, _ = newTestTrial(t, Automatic, &standIn{replies: []string{answerReply(nil)}})
	tr.MeshCheck = func(context.Context) error { return errors.New("no fused edge to billing") }
	if r := tr.Run(context.Background()); r.Passed || r.Score != 1 || r.Mesh {
		t.Errorf("no mesh: %+v", r)
	}
}

// TestTerminalNeverEmpty: a command that succeeds silently still gives
// the agent a message. The first real trial (task nm4) sent claude an
// empty message after `ssh iom2 true`, and `claude -p --resume` with an
// empty prompt failed the run ("No deferred tool marker found").
func TestTerminalNeverEmpty(t *testing.T) {
	tests := []struct {
		text string
		code int
		err  error
		want string
	}{
		{"", 0, nil, "[no output, exit status 0]\n"},
		{"out\n", 0, nil, "out\n"},
		{"", 3, nil, "[exit status 3]\n"},
		{"", 0, errors.New("ssh: broken"), "[ssh: broken]\n"},
	}
	for _, tt := range tests {
		if got := terminal(tt.text, tt.code, tt.err); got != tt.want {
			t.Errorf("terminal(%q, %d, %v) = %q, want %q", tt.text, tt.code, tt.err, got, tt.want)
		}
	}
}

// TestCheckEdges: a fused edge to each service is needed; an unresolved
// or missing one fails.
func TestCheckEdges(t *testing.T) {
	if err := CheckEdges(fakeEdges, MeshServices); err != nil {
		t.Error(err)
	}
	partial := strings.Replace(fakeEdges, "billing    fused", "billing    mixed", 1)
	if err := CheckEdges(partial, MeshServices); err == nil || !strings.Contains(err.Error(), "billing") {
		t.Errorf("mixed edge to billing: %v", err)
	}
	if err := CheckEdges(spacedEdges, MeshServices); err != nil {
		t.Errorf("a FROM with spaces: %v", err)
	}
	spacedPartial := strings.Replace(spacedEdges, "orders     fused", "orders     mixed", 1)
	if err := CheckEdges(spacedPartial, MeshServices); err == nil || !strings.Contains(err.Error(), "orders") {
		t.Errorf("a FROM with spaces, mixed edge to orders: %v", err)
	}
}

// spacedEdges is `show edges` as the first real trial printed it (task
// nm4): the gateway, a client with no port, is named by its pattern,
// spaces and all, so the columns are not whitespace-separated fields.
const spacedEdges = `FROM                                         TO         STATE  CONNS  UNRESOLVED  FIRST                LAST
app:gateway/*microservices --role gateway *  inventory  fused  9      0           2026-11-26 20:42:20  2026-11-26 20:46:47
app:gateway/*microservices --role gateway *  orders     fused  9      0           2026-11-26 20:42:17  2026-11-26 20:46:47
orders                                       billing    fused  9      0           2026-11-26 20:42:19  2026-11-26 20:46:47
orders                                       inventory  fused  9      0           2026-11-26 20:42:18  2026-11-26 20:46:46
`

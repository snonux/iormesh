package eval

import (
	"context"
	"fmt"
	"strings"
)

// Session is what every question of a run shares: the set, the rollup
// text and source listing the prompt carries, the model and the tools.
type Session struct {
	Set      Set
	Report   string
	Sources  []string
	Model    Model
	Tools    Tools
	MaxTurns int
}

// Result is one run of a question set.
type Result struct {
	Scenario string `json:"scenario"`
	Date     string `json:"date"`
	// Model is the operator's label for the model (-model-name), never
	// the command line: that may carry credentials.
	Model     string           `json:"model"`
	Threshold float64          `json:"threshold"`
	Score     float64          `json:"score"`
	Passed    bool             `json:"passed"`
	Questions []QuestionResult `json:"questions"`
}

// QuestionResult is one question's outcome.
type QuestionResult struct {
	ID     string `json:"id"`
	Passed bool   `json:"passed"`
	Answer string `json:"answer"`
	Turns  int    `json:"turns"`
	// Actions are the QUERY and SOURCE lines the model ran, in order.
	Actions  []string `json:"actions"`
	Failures []string `json:"failures"`
}

// Run asks every question in order, each with a fresh transcript, and
// scores the set. A model command that fails is a failed question, not
// a failed run: the score counts it.
func (s Session) Run(ctx context.Context, date, model string) Result {
	r := Result{Scenario: s.Set.Scenario, Date: date, Model: model, Threshold: s.Set.Threshold,
		Questions: []QuestionResult{}}
	passed := 0
	for _, q := range s.Set.Questions {
		qr := s.Ask(ctx, q)
		if qr.Passed {
			passed++
		}
		r.Questions = append(r.Questions, qr)
	}
	r.Score = float64(passed) / float64(len(s.Set.Questions))
	r.Passed = r.Score >= s.Set.Threshold
	return r
}

// Ask runs one question's turns until the model answers or the turn
// limit is reached, then scores the answer.
func (s Session) Ask(ctx context.Context, q Question) QuestionResult {
	qr := QuestionResult{ID: q.ID, Actions: []string{}, Failures: []string{}}
	var transcript []string
	for qr.Turns < s.MaxTurns {
		qr.Turns++
		reply, err := s.Model.Reply(ctx, s.prompt(q, transcript, qr.Turns == s.MaxTurns))
		if err != nil {
			qr.Failures = append(qr.Failures, err.Error())
			return qr
		}
		act, ok := ParseReply(reply)
		if !ok {
			transcript = append(transcript, "(your reply held no action: reply with one QUERY, SOURCE or ANSWER: line)")
			continue
		}
		if act.Kind == ActAnswer {
			qr.Answer = act.Arg
			qr.Failures = append(qr.Failures, q.Score(act.Arg)...)
			qr.Passed = len(qr.Failures) == 0
			return qr
		}
		line := act.Kind + " " + act.Arg
		qr.Actions = append(qr.Actions, line)
		out, err := s.run(ctx, act)
		if err != nil {
			qr.Failures = append(qr.Failures, err.Error())
			return qr
		}
		transcript = append(transcript, "> "+line+"\n"+out)
	}
	qr.Failures = append(qr.Failures, fmt.Sprintf("no answer within %d turns", s.MaxTurns))
	return qr
}

// run executes a QUERY or SOURCE action.
func (s Session) run(ctx context.Context, act Action) (string, error) {
	if act.Kind == ActSource {
		return s.Tools.Source(act.Arg)
	}
	flags, err := SplitArgs(act.Arg)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	return s.Tools.Query(ctx, flags)
}

// promptHead explains the task and the protocol.
const promptHead = `You are answering a question about a traced Java system from its iormesh trace.
You see the trace's per-run summary rollup below. Each turn, reply with exactly one action line:

QUERY <flags>    events of the merged trace as NDJSON (iormeshctl query), flags:
                 -from/-to TIME, -host GLOB, -app GLOB, -thread GLOB (Java thread name or tid),
                 -conn ID [-follow], -file GLOB, -method GLOB (class#method)
                 globs match the whole string and '*' stops at '/': a -file glob names
                 every directory level, e.g. QUERY -file '/tmp/*/*/orders.d/order-42.json*'
SOURCE <path>    a file of the workload's source, path from the list below
ANSWER: <text>   your final answer: concise, naming only what the question asks

Notes: a served request's http event is written when its response completes, so on the
serving thread the calls it made come before it. java.stack lists method ids; the rollup
names each file's and each process's causing methods (class#method).
`

// prompt builds one turn's prompt: the head, the rollup, the sources,
// the transcript so far and the question.
func (s Session) prompt(q Question, transcript []string, last bool) string {
	var b strings.Builder
	b.WriteString(promptHead)
	b.WriteString("\n== Summary rollup ==\n")
	b.WriteString(s.Report)
	b.WriteString("\n== Workload source files ==\n")
	b.WriteString(strings.Join(s.Sources, "\n"))
	b.WriteString("\n")
	if len(transcript) > 0 {
		b.WriteString("\n== Your actions so far ==\n")
		b.WriteString(strings.Join(transcript, "\n"))
		b.WriteString("\n")
	}
	b.WriteString("\n== Question ==\n")
	b.WriteString(q.Question)
	b.WriteString("\n")
	if last {
		b.WriteString("\nThis is your last turn: reply with ANSWER: now.\n")
	}
	return b.String()
}

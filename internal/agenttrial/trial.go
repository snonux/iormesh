package agenttrial

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"iormesh/internal/eval"
)

// The modes of the skill (decision #37, "The skill", rule 2).
const (
	Automatic = "automatic"
	Guided    = "guided"
)

// The budget of decision #37 ("Budget and score").
const (
	DefaultMaxTurns = 150
	DefaultTimeout  = 60 * time.Minute
)

// The scripted user's only words (decision #37, "Help"): the mode choice
// and the approval answers of the skill's form. In guided mode an
// approval is followed by the verbatim output of the named commands.
const (
	approveWord = "yes"
	changeWord  = "change"
	// noAction is the harness's frame, not the user's: a reply with no
	// action gets it, and it says nothing about the task.
	noAction = "(harness: your reply held no explanation, RUN, READ or ANSWER line. The user " +
		"answers only the mode question and explanations, with the outputs in guided mode.)"
)

// Trial is one run of one agent in one mode.
type Trial struct {
	Agent Agent
	// Shell reaches the VMs; its Hosts are the hosts a step may run on.
	Shell *Shell
	// Library serves READ from the copy of the docs (CopyLibrary).
	Library  eval.SourceTree
	Set      eval.Set
	Mode     string
	MaxTurns int
	Timeout  time.Duration
	// First is the first message: the task prompt, the skill, the
	// library's listing and the questions (FirstMessage).
	First string
	// Transcript receives one JSON line per message (TranscriptLine).
	Transcript io.Writer
	// MeshCheck proves the install and the trace after the answers: a
	// built mesh holding the workload's cross-service edges (#37).
	MeshCheck func(ctx context.Context) error
}

// TranscriptLine is one message of the run.
type TranscriptLine struct {
	Turn int    `json:"turn"`
	From string `json:"from"` // agent or user
	Text string `json:"text"`
	Time string `json:"time"`
}

// state is what the scripted user remembers.
type state struct {
	modeGiven bool
	step      int
	approved  *Explanation
	// cwd is guided mode's terminal directory per host (typedCommand):
	// empty until a typed command reports one, the login directory.
	cwd map[string]string
}

// Run plays the trial: each agent reply is a turn, the scripted user
// answers it, until the agent answers the questions, breaks a rule or
// runs out of turns or time. The answers are then scored and the mesh
// checked; both must pass.
func (t *Trial) Run(ctx context.Context) Result {
	start := time.Now()
	r := Result{Mode: t.Mode, Threshold: t.Set.Threshold, MaxTurns: t.MaxTurns,
		Questions: []Answer{}}
	budget, cancel := context.WithTimeout(ctx, t.Timeout)
	defer cancel()
	answers := t.converse(budget, &r)
	r.Minutes = time.Since(start).Minutes()
	if r.Failure == "" {
		t.finish(ctx, &r, answers)
	}
	r.Passed = r.Failure == "" && r.Mesh && r.Score >= r.Threshold
	return r
}

// converse runs the turns and returns the answers, or sets r.Failure.
func (t *Trial) converse(ctx context.Context, r *Result) map[string]string {
	st := &state{}
	msg := DefuseMentions(t.First)
	t.record(0, "user", msg)
	for r.Turns < t.MaxTurns {
		r.Turns++
		reply, err := t.Agent.Reply(ctx, msg)
		t.record(r.Turns, "agent", reply)
		if ctx.Err() != nil {
			r.Failure = fmt.Sprintf("time budget: past %v", t.Timeout)
			return nil
		}
		if err != nil {
			r.Failure = "agent: " + err.Error()
			return nil
		}
		next, answers, fail := t.respond(ctx, st, reply)
		switch {
		case ctx.Err() != nil:
			r.Failure = fmt.Sprintf("time budget: past %v", t.Timeout)
			return nil
		case fail != "":
			r.Failure = fail
			return nil
		case answers != nil:
			return answers
		}
		msg = DefuseMentions(next)
		t.record(r.Turns, "user", msg)
	}
	r.Failure = fmt.Sprintf("turn budget: no answers within %d turns", t.MaxTurns)
	return nil
}

// respond is the scripted user's (or the harness's) answer to a reply:
// the next message, the answers when the reply held them, or a failure.
func (t *Trial) respond(ctx context.Context, st *state, reply string) (string, map[string]string, string) {
	rep := Classify(reply, t.Shell.Hosts)
	switch {
	case rep.Kind == ActAnswer:
		return "", rep.Answers, ""
	case rep.Kind == ActRead:
		out, err := t.Library.Source(rep.Arg)
		if err != nil {
			return "", nil, "library: " + err.Error()
		}
		return out, nil, ""
	case !st.modeGiven:
		// The skill asks for the mode first; until it is given, every
		// message gets the mode choice and nothing is approved.
		st.modeGiven = true
		return "Mode: " + t.Mode + ".", nil, ""
	case rep.Kind == ActExplain:
		return t.approve(ctx, st, rep.Explanation)
	case rep.Kind == ActRun:
		return t.runCommand(ctx, st, rep.Arg)
	}
	return noAction, nil, ""
}

// approve answers an explanation: "change" when it lacks the host, the
// commands, the change or the outgoing data, else "yes" — in guided mode
// followed by the output of the named commands, typed by the user.
func (t *Trial) approve(ctx context.Context, st *state, e Explanation) (string, map[string]string, string) {
	if len(e.Missing) > 0 {
		return changeWord, nil, ""
	}
	st.step++
	st.approved = &e
	if t.Mode == Automatic {
		return approveWord, nil, ""
	}
	return approveWord + "\n\n" + t.typeCommands(ctx, st), nil, ""
}

// typeCommands is guided mode's user at the terminal: exactly the
// approved commands, in order, on the named host, and their output
// pasted back as the terminal showed it. A user types into one terminal
// per host, so a `cd` carries to the next command and the next step
// (typedCommand); the log shows each command as typed.
func (t *Trial) typeCommands(ctx context.Context, st *state) string {
	if st.cwd == nil {
		st.cwd = map[string]string{}
	}
	host := st.approved.Host
	var out string
	for _, c := range st.approved.Commands {
		text, code, err := t.Shell.Run(ctx, Call{Host: host, Command: typedCommand(st.cwd[host], c),
			LogAs: c, By: ByUser, Step: st.step})
		text, st.cwd[host] = splitTyped(text, st.cwd[host])
		out += "$ " + c + "\n" + terminal(text, code, err)
	}
	return out
}

// cwdMark starts the line typedCommand appends to report the directory
// a command left its shell in: an ASCII record separator, which no
// command's own output is expected to hold.
const cwdMark = "\n\x1ecwd="

// typedCommand wraps a typed command so it runs in the terminal's
// directory cwd (none: the login directory) and reports the directory it
// leaves, keeping its exit status. A command that exits its shell
// reports nothing and the directory stays. The command sits on its own
// lines inside braces, so a trailing comment or `&` stays its own.
func typedCommand(cwd, command string) string {
	cd := ""
	if cwd != "" {
		cd = "cd " + shellQuote(cwd) + " && "
	}
	return cd + "{\n" + command + "\n}; s=$?; printf '" + strings.ReplaceAll(cwdMark, "\n", `\n`) +
		"%s\\n' \"$PWD\"; exit $s"
}

// splitTyped takes typedCommand's report off a command's output and
// returns the output and the directory, cwd when there is no report.
func splitTyped(out, cwd string) (string, string) {
	i := strings.LastIndex(out, cwdMark)
	if i < 0 {
		return out, cwd
	}
	return out[:i], strings.TrimSuffix(out[i+len(cwdMark):], "\n")
}

// shellQuote quotes s as one POSIX shell word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// runCommand is automatic mode's RUN: only a command the latest approved
// explanation named runs; anything else fails the run before it runs
// (#37: "A command run that the latest approved explanation did not name
// fails the run"). In guided mode the agent runs nothing itself.
func (t *Trial) runCommand(ctx context.Context, st *state, command string) (string, map[string]string, string) {
	switch {
	case t.Mode == Guided:
		return "", nil, fmt.Sprintf("guided mode: the agent asked to run %q itself", command)
	case st.approved == nil:
		return "", nil, fmt.Sprintf("RUN %q before any approved explanation", command)
	case !st.approved.Names(command):
		return "", nil, fmt.Sprintf("RUN %q: the latest approved explanation (step %d) did not name it",
			command, st.step)
	}
	text, code, err := t.Shell.Run(ctx, Call{Host: st.approved.Host, Command: command, By: ByAgent,
		Step: st.step})
	return terminal(text, code, err), nil, ""
}

// terminal is a command's output as a terminal shows it, with a non-zero
// exit status or an ssh failure noted after it, and a silent success
// noted as such: the message is never empty. An empty message is no
// prompt to `claude -p --resume`, which then fails the run ("No deferred
// tool marker found"), as the first real trial's `ssh iom2 true` did
// (task nm4).
func terminal(text string, code int, err error) string {
	switch {
	case err != nil:
		return text + "[" + err.Error() + "]\n"
	case code != 0:
		return text + fmt.Sprintf("[exit status %d]\n", code)
	case text == "":
		return "[no output, exit status 0]\n"
	}
	return text
}

// mentionStart is an "@" that starts a word: what an agent CLI may read
// as a file mention ("@/path").
var mentionStart = regexp.MustCompile(`(^|\s)@`)

// DefuseMentions escapes every "@" that starts a word as "\@", in every
// message the harness sends the agent. Belt and braces to ClaudeEnv's
// CLAUDE_CODE_DISABLE_FILE_MENTIONS (review round 1 of task mm4): the
// messages carry VM output and docs the agent can steer, and no CLI may
// take a word of them for a laptop file to attach. The choice: a visible
// backslash, which a model reads as an escape, over an invisible or
// look-alike character that would change the output silently; "@"
// inside a word (user@host) is left alone, it is no mention.
func DefuseMentions(s string) string {
	return mentionStart.ReplaceAllString(s, `$1\@`)
}

// record writes one transcript line; a write error is dropped, as in
// Shell.log.
func (t *Trial) record(turn int, from, text string) {
	if t.Transcript == nil {
		return
	}
	_, _ = t.Transcript.Write(jsonLine(TranscriptLine{Turn: turn, From: from, Text: text,
		Time: time.Now().UTC().Format(time.RFC3339)}))
}

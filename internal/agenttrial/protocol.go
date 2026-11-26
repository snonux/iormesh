package agenttrial

import (
	"regexp"
	"strings"
)

// Action kinds of an agent's reply (the package comment's protocol).
const (
	ActNone    = ""
	ActExplain = "EXPLAIN" // an explanation in the skill's form
	ActRun     = "RUN"     // RUN <command>: run one approved command
	ActRead    = "READ"    // READ <path>: a file of the docs copy
	ActAnswer  = "ANSWER"  // ANSWER <id>: <text>, one per question
)

// Reply is one agent reply, classified.
type Reply struct {
	Kind        string
	Arg         string            // RUN's command, READ's path
	Explanation Explanation       // ActExplain
	Answers     map[string]string // ActAnswer: question id -> answer
}

var answerLine = regexp.MustCompile(`^ANSWER\s+([A-Za-z0-9_.-]+)\s*:\s?(.*)$`)

// Classify reads one reply. The answers win (they end the run), then an
// explanation (the scripted user answers it before anything else runs),
// then the first RUN or READ line. Code fences are ignored around all of
// them; anything else is ActNone.
func Classify(reply string, hosts []string) Reply {
	if a := parseAnswers(reply); len(a) > 0 {
		return Reply{Kind: ActAnswer, Answers: a}
	}
	if e, ok := ParseExplanation(reply, hosts); ok {
		return Reply{Kind: ActExplain, Explanation: e}
	}
	for _, l := range strings.Split(reply, "\n") {
		l = strings.TrimSpace(l)
		for _, kind := range []string{ActRun, ActRead} {
			if arg, ok := strings.CutPrefix(l, kind+" "); ok && strings.TrimSpace(arg) != "" {
				return Reply{Kind: kind, Arg: unquote(arg)}
			}
		}
	}
	return Reply{Kind: ActNone}
}

// parseAnswers collects "ANSWER <id>: text" sections: each runs to the
// next ANSWER line or the end of the reply, fences dropped.
func parseAnswers(reply string) map[string]string {
	out := map[string]string{}
	id := ""
	var cur []string
	flush := func() {
		if id != "" {
			out[id] = strings.TrimSpace(strings.Join(cur, "\n"))
		}
	}
	for _, line := range strings.Split(reply, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "```") {
			continue
		}
		if m := answerLine.FindStringSubmatch(l); m != nil {
			flush()
			id, cur = m[1], []string{m[2]}
			continue
		}
		if id != "" {
			cur = append(cur, line)
		}
	}
	flush()
	return out
}

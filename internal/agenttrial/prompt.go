package agenttrial

import (
	"fmt"
	"strings"

	"iormesh/internal/eval"
)

// SystemPrompt replaces the agent CLI's own (claude --system-prompt, pi
// --system-prompt): it says how the agent acts, never what to do — that
// is the task prompt's and the skill's.
const SystemPrompt = `You are an agent working with a human user through the text protocol below.
You have no tools of your own. Every message you write is one turn; the reply you get is
the user's answer, or the harness's result of your action. Each message holds exactly one
of these:

- An explanation of a step, in the iormesh skill's form ("Runs on:", "Commands:" as a
  numbered list, "Changes:", "Data leaving this machine:"). Then stop and wait: the user
  answers yes, no or change.
- RUN <command>      one command of the latest approved step, exactly as its explanation
                     wrote it; it runs on that step's host and you get its output
                     (automatic mode only: in guided mode you run nothing, the user
                     types the commands and pastes their output after the "yes").
- READ <path>        a file of the documentation copy (paths are listed in the first
                     message; a link's ../ resolves as usual). Reading needs no approval.
- ANSWER <id>: <text>  your answers, one ANSWER line per question, all in your final
                     message. The final message ends the session.

Any other message reaches the user as plain text.`

// FirstMessage is the first turn's message: the task prompt with its
// placeholders filled, the skill as a skill loader would place it in
// context, the library's file list and the questions.
func FirstMessage(task, skill string, files []string, set eval.Set, vars map[string]string) string {
	var b strings.Builder
	b.WriteString(FillTask(task, vars))
	b.WriteString("\n\n== The iormesh skill (" + SkillFile + ") ==\n\n")
	b.WriteString(skill)
	b.WriteString("\n\n== Files you can READ ==\n\n")
	b.WriteString(strings.Join(files, "\n"))
	b.WriteString("\n\n== Questions ==\n\n")
	for _, q := range set.Questions {
		fmt.Fprintf(&b, "[%s] %s\n", q.ID, strings.TrimSpace(q.Question))
	}
	return b.String()
}

// FillTask replaces each {{name}} of the task prompt with vars[name].
func FillTask(task string, vars map[string]string) string {
	for k, v := range vars {
		task = strings.ReplaceAll(task, "{{"+k+"}}", v)
	}
	return task
}

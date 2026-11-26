package eval

import "regexp"

// mentionStart is an "@" that starts a word: what an agent CLI may read
// as a file mention ("@/path").
var mentionStart = regexp.MustCompile(`(^|\s)@`)

// DefuseMentions escapes every "@" that starts a word as "\@". Claude
// Code expands an "@<path>" in its prompt into that file's contents,
// attached to the request, even with its tools set to the empty list
// (found in review round 1 of task mm4) — and the text both harnesses
// send carries what neither controls: here the trace's rollup, query
// slices and workload source
// (task wm4), in internal/agenttrial VM output and docs the agent can
// steer. No CLI may take a word of it for a laptop file to attach. The
// choice: a visible backslash, which a model reads as an escape, over an
// invisible or look-alike character that would change the text silently;
// an "@" inside a word (user@host) is left alone, it is no mention.
func DefuseMentions(s string) string {
	return mentionStart.ReplaceAllString(s, `$1\@`)
}

// MentionGuardEnv is added to every model command's environment (task
// wm4): Claude Code's file mentions off and the CLI confined, the two
// variables shown to stop the expansion against a capture server
// (internal/agenttrial's TestClaudeFileMention). The operator names the
// command, so the harness cannot know it is claude; the variables are
// set for every command — inherited through `env -C /` or a wrapper
// script — and mean nothing to any other model CLI.
var MentionGuardEnv = []string{"CLAUDE_CODE_DISABLE_FILE_MENTIONS=1", "CLAUDE_CODE_EVAL_CONFINED=1"}

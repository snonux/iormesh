package agenttrial

import (
	"regexp"
	"slices"
	"strings"
)

// Explanation is a step as the skill writes it before asking for
// approval (docs/skills/iormesh/SKILL.md, "The rule: explain, then
// wait"): the host it runs on, the commands, the change and the data
// leaving the machine. The scripted user approves one only when it names
// all four (decision #37, "Approval in the unattended trial").
type Explanation struct {
	Host     string
	Commands []string
	Changes  string
	Data     string
	// Missing lists what the explanation did not name; empty when it is
	// complete.
	Missing []string
}

// The labels of the skill's form, lower-cased. "data leaving" also takes
// the skill's longer "data leaving this machine".
const (
	labelRunsOn   = "runs on"
	labelCommands = "commands"
	labelChanges  = "changes"
	labelData     = "data leaving"
)

var (
	labelLine    = regexp.MustCompile(`^([a-z][a-z ]*?)\s*:\s*(.*)$`)
	numberedLine = regexp.MustCompile(`^\d+[.)]\s+(.+)$`)
)

// ParseExplanation reads an explanation out of a reply. ok is false when
// the reply holds none (no "Commands:" label); otherwise e.Missing says
// what the scripted user would not approve. hosts are the VMs a step may
// run on: "Runs on" must name exactly one of them.
func ParseExplanation(reply string, hosts []string) (e Explanation, ok bool) {
	lines := cleanLines(reply)
	for i, l := range lines {
		label, value, isLabel := splitLabel(l)
		if !isLabel {
			continue
		}
		switch {
		case label == labelRunsOn:
			e.Host = namedHost(value, hosts)
		case label == labelCommands:
			ok = true
			e.Commands = numberedItems(lines[i+1:])
		case label == labelChanges:
			e.Changes = valueOrNext(value, lines[i+1:])
		case strings.HasPrefix(label, labelData):
			e.Data = valueOrNext(value, lines[i+1:])
		}
	}
	e.Missing = missing(e)
	return e, ok
}

// missing names the parts an explanation lacks.
func missing(e Explanation) []string {
	var m []string
	if e.Host == "" {
		m = append(m, "the host (Runs on: one of the VMs)")
	}
	if len(e.Commands) == 0 {
		m = append(m, "the commands (a numbered list under Commands:)")
	}
	if e.Changes == "" {
		m = append(m, "what changes (Changes:)")
	}
	if e.Data == "" {
		m = append(m, "the data leaving the machine (Data leaving this machine:)")
	}
	return m
}

// cleanLines splits a reply into lines with code fences dropped and each
// line trimmed of indentation and of the Markdown a model may wrap a
// label in ("**Runs on:**", "- Changes:").
func cleanLines(reply string) []string {
	var out []string
	for _, l := range strings.Split(reply, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "```") {
			continue
		}
		out = append(out, l)
	}
	return out
}

// splitLabel splits "Label: value" into the lower-cased label and the
// value; Markdown emphasis and a list dash around the label are ignored.
func splitLabel(l string) (label, value string, ok bool) {
	plain := strings.TrimLeft(l, "-*> ")
	plain = strings.Replace(plain, "**", "", 2)
	m := labelLine.FindStringSubmatch(strings.ToLower(plain))
	if m == nil {
		return "", "", false
	}
	// The value keeps its case: take it from the original after the colon.
	_, v, _ := strings.Cut(plain, ":")
	return m[1], strings.TrimSpace(strings.Trim(strings.TrimSpace(v), "*")), true
}

// valueOrNext is a label's value, or the next line's when the label
// stands alone, unless that line is a label of its own.
func valueOrNext(value string, rest []string) string {
	if value != "" || len(rest) == 0 {
		return value
	}
	if _, _, isLabel := splitLabel(rest[0]); isLabel {
		return ""
	}
	return rest[0]
}

// numberedItems are the "1. command" lines that follow, up to the first
// line that is neither numbered nor blank. A command wrapped in
// backquotes is unwrapped: the backquotes are Markdown, not shell.
func numberedItems(rest []string) []string {
	var items []string
	for _, l := range rest {
		if l == "" {
			if len(items) == 0 {
				continue
			}
			break
		}
		m := numberedLine.FindStringSubmatch(l)
		if m == nil {
			break
		}
		items = append(items, unquote(m[1]))
	}
	return items
}

// unquote strips one pair of Markdown backquotes around a command.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && strings.HasPrefix(s, "`") && strings.HasSuffix(s, "`") && !strings.Contains(s[1:len(s)-1], "`") {
		return strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

// namedHost is the one VM the "Runs on" value names before its ";"
// (the skill writes "Runs on: <machine>; contacts: …"), or "" when it
// names none or several.
func namedHost(value string, hosts []string) string {
	on, _, _ := strings.Cut(value, ";")
	var found []string
	for _, w := range strings.FieldsFunc(on, func(r rune) bool {
		return !(r == '-' || r == '.' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	}) {
		if slices.Contains(hosts, w) && !slices.Contains(found, w) {
			found = append(found, w)
		}
	}
	if len(found) != 1 {
		return ""
	}
	return found[0]
}

// Names reports whether the explanation names command exactly (after
// trimming surrounding space), as the skill's rule asks: "Run only the
// commands the approved explanation names, exactly as written."
func (e Explanation) Names(command string) bool {
	return slices.Contains(e.Commands, strings.TrimSpace(command))
}

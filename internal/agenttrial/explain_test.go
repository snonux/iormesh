package agenttrial

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

var vms = []string{"iom1", "iom2"}

// skillStep is an explanation exactly in the skill's form
// (docs/skills/iormesh/SKILL.md), fenced as the skill shows it.
const skillStep = "Here is the first step.\n\n```text\n" +
	"Step 1: Check controller and hosts (guide chapter 2)\n" +
	"Runs on: iom1; contacts: iom2\n" +
	"Commands:\n" +
	"  1. `ssh -o BatchMode=yes iom2 true`\n" +
	"  2. rsync --version\n" +
	"Changes: nothing\n" +
	"Data leaving this machine: command output goes to my model provider\n" +
	"Success: exit 0 and a version line\n" +
	"Approve? yes / no / change\n```\n"

// TestParseExplanationSkillForm: the skill's form parses whole, the
// Markdown backquotes around a command dropped.
func TestParseExplanationSkillForm(t *testing.T) {
	e, ok := ParseExplanation(skillStep, vms)
	if !ok || len(e.Missing) != 0 {
		t.Fatalf("ok %v, missing %v", ok, e.Missing)
	}
	want := []string{"ssh -o BatchMode=yes iom2 true", "rsync --version"}
	if e.Host != "iom1" || !slices.Equal(e.Commands, want) || e.Changes != "nothing" || e.Data == "" {
		t.Errorf("parsed %+v", e)
	}
	if !e.Names("rsync --version") || e.Names("rsync --version; id") || e.Names("rsync") {
		t.Error("Names must match a named command exactly")
	}
}

// TestParseExplanationMissing: an explanation without one of the four
// parts is not approvable, and says which part it lacks; a reply without
// a Commands label is no explanation at all.
func TestParseExplanationMissing(t *testing.T) {
	for name, tc := range map[string]struct {
		reply   string
		missing int
	}{
		"no host line":        {"Commands:\n1. true\nChanges: none\nData leaving: none\n", 1},
		"host not a VM":       {"Runs on: laptop\nCommands:\n1. true\nChanges: none\nData leaving: none\n", 1},
		"two hosts":           {"Runs on: iom1 and iom2\nCommands:\n1. true\nChanges: none\nData leaving: none\n", 1},
		"no numbered command": {"Runs on: iom1\nCommands: true\nChanges: none\nData leaving: none\n", 1},
		"no changes":          {"Runs on: iom1\nCommands:\n1. true\nData leaving: none\n", 1},
		"no data":             {"Runs on: iom1\nCommands:\n1. true\nChanges: none\n", 1},
		"only commands":       {"Commands:\n1. true\n", 3},
		"markdown labels":     {"**Runs on:** iom2\n- **Commands:**\n  1. uptime\n**Changes:** none\n**Data leaving this machine:**\n  uptime's output\n", 0},
	} {
		e, ok := ParseExplanation(tc.reply, vms)
		if !ok || len(e.Missing) != tc.missing {
			t.Errorf("%s: ok %v, missing %v, want %d", name, ok, e.Missing, tc.missing)
		}
	}
	if _, ok := ParseExplanation("Which mode do you want: automatic or guided?", vms); ok {
		t.Error("a question parsed as an explanation")
	}
}

// TestParseExplanationProseInCommand: a Commands item holds the command
// alone. glm-5.3-flash's approved step listed "sudo dnf install -y tar —
// only if command 1 shows no tar", then ran the bare command, which no
// item named (task bn4); guided mode would have typed the prose into
// dnf. Such an item is not approvable, with the item named; a plain
// command with its own dashes, `--`, `#` or `if` stays approvable.
func TestParseExplanationProseInCommand(t *testing.T) {
	const form = "Runs on: iom1\nCommands:\n  1. %s\nChanges: none\nData leaving: none\n"
	for _, item := range []string{
		"sudo dnf install -y tar — only if command 1 shows no tar",
		"`ssh iom2 ps -eo args` – only if command 3 finds nothing",
		"sudo dnf install -y tar (only if tar is missing)",
		"sudo dnf install -y tar, only if command 1 shows no tar",
		"`sudo dnf install -y tar` (if needed)",
	} {
		e, ok := ParseExplanation(fmt.Sprintf(form, item), vms)
		if !ok || len(e.Missing) != 1 || !strings.Contains(e.Missing[0], "item 1") {
			t.Errorf("%q: ok %v, missing %v, want the item named as prose", item, ok, e.Missing)
		}
	}
	for _, item := range []string{
		"rpm -q tar || sudo dnf install -y tar",
		"git log -1 -- docs",
		"if command -v tar; then echo yes; fi",
		"ssh -o BatchMode=yes iom2 true # check the key",
		"tar -xzf iormesh-0.1.0-linux-amd64.tar.gz",
	} {
		e, ok := ParseExplanation(fmt.Sprintf(form, item), vms)
		if !ok || len(e.Missing) != 0 {
			t.Errorf("%q: ok %v, missing %v, want approvable", item, ok, e.Missing)
		}
	}
}

// TestClassify: answers first, then an explanation, then the first RUN
// or READ line; prose is no action.
func TestClassify(t *testing.T) {
	for name, tc := range map[string]struct {
		reply, kind, arg string
	}{
		"run":              {"Running it now.\nRUN `rsync --version`\n", ActRun, "rsync --version"},
		"read":             {"READ docs/guide/03-release.md", ActRead, "docs/guide/03-release.md"},
		"explain over run": {skillStep + "\nRUN rsync --version\n", ActExplain, ""},
		"prose":            {"Which mode would you like?", ActNone, ""},
		"bare RUN":         {"RUN \n", ActNone, ""},
	} {
		r := Classify(tc.reply, vms)
		if r.Kind != tc.kind || r.Arg != tc.arg {
			t.Errorf("%s: %q %q, want %q %q", name, r.Kind, r.Arg, tc.kind, tc.arg)
		}
	}
	r := Classify("Done.\n```\nANSWER a: first\nline two\nANSWER b-2: second\n```\n", vms)
	if r.Kind != ActAnswer || r.Answers["a"] != "first\nline two" || r.Answers["b-2"] != "second" {
		t.Errorf("answers %+v", r)
	}
}

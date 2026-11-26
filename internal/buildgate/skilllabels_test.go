package buildgate

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"iormesh/internal/agenttrial"
)

// The skill's explanation form (docs/skills/iormesh/SKILL.md, section 2)
// is what the M8 trial's scripted user approves (decision #37): it reads
// the reply with agenttrial.ParseExplanation, which finds a part only by
// its label at the start of a line. In the trial a model wrote "Changes,
// all on iom1:" in place of "Changes:" and was told "change" six times
// (task 4n4). These gates hold the skill's labels to the parser's, and
// the skill to saying that the labels are written verbatim at line start.

// skillLabels are the labels of the skill's form, in its order. The
// trial's parser needs the first four; "Success:" is the skill's own.
var skillLabels = []string{"Runs on:", "Commands:", "Changes:", "Data leaving this machine:", "Success:"}

// trialLabels is how many of skillLabels the trial's parser requires.
const trialLabels = 4

// templateHost stands in for the template's <machine> placeholder: the
// parser accepts only a host it was given.
const templateHost = "iom1"

// textBlock is a fenced text block's body.
var textBlock = regexp.MustCompile("(?s)```text\n(.*?)```")

// skillTemplate returns the form in SKILL.md: the text block that
// starts with "Step N:".
func skillTemplate(t *testing.T, skill string) string {
	t.Helper()
	for _, m := range textBlock.FindAllStringSubmatch(skill, -1) {
		if strings.HasPrefix(m[1], "Step N:") {
			return m[1]
		}
	}
	t.Fatal("SKILL.md: no text block starting with \"Step N:\" (the explanation form)")
	return ""
}

// approvable reports whether the trial's scripted user would approve an
// explanation naming hosts, and what it misses when not.
func approvable(text string, hosts []string) (bool, []string) {
	e, ok := agenttrial.ParseExplanation(text, hosts)
	if !ok {
		return false, []string{"the Commands: label"}
	}
	return len(e.Missing) == 0, e.Missing
}

// templateProblems returns what is wrong with an explanation form: a
// label of skillLabels that does not start a line exactly as written, or
// a form the trial's parser would not approve.
func templateProblems(tmpl string) []string {
	var problems []string
	lines := strings.Split(tmpl, "\n")
	for _, label := range skillLabels {
		found := false
		for _, l := range lines {
			found = found || strings.HasPrefix(l, label)
		}
		if !found {
			problems = append(problems, "no line starts with "+label)
		}
	}
	filled := strings.Replace(tmpl, "<machine>", templateHost, 1)
	if ok, missing := approvable(filled, []string{templateHost}); !ok {
		problems = append(problems, "the trial would not approve it: missing "+strings.Join(missing, ", "))
	}
	return problems
}

// TestSkillTemplateLabelsAreTheTrials: the skill's form starts a line
// with each label and the trial approves it; dropping any one of the
// labels the trial requires makes it unapprovable, so the skill names
// exactly the labels the trial checks.
func TestSkillTemplateLabelsAreTheTrials(t *testing.T) {
	tmpl := skillTemplate(t, skillFiles(t)[filepath.Join(skillDir, "SKILL.md")])
	for _, p := range templateProblems(tmpl) {
		t.Errorf("SKILL.md form: %s", p)
	}
	filled := strings.Replace(tmpl, "<machine>", templateHost, 1)
	for _, label := range skillLabels[:trialLabels] {
		var kept []string
		for _, l := range strings.Split(filled, "\n") {
			if !strings.HasPrefix(l, label) {
				kept = append(kept, l)
			}
		}
		if ok, _ := approvable(strings.Join(kept, "\n"), []string{templateHost}); ok {
			t.Errorf("the trial approves the form without %q: the skill's labels and the trial's differ", label)
		}
	}
}

// TestSkillTemplateGateCatchesRenamedLabel is the negative side: the
// trial's "Changes, all on iom1:" fails the line-start rule.
func TestSkillTemplateGateCatchesRenamedLabel(t *testing.T) {
	bad := "Step N: x\nRuns on: <machine>; contacts: nothing\nCommands:\n  1. true\n" +
		"Changes, all on iom1: nothing\nData leaving this machine: nothing\nSuccess: exit 0\n"
	if p := templateProblems(bad); len(p) == 0 || !strings.Contains(p[0], "Changes:") {
		t.Errorf("a renamed Changes label: got %q, want it reported", p)
	}
}

// TestSkillStatesLabelsVerbatim: outside the form, SKILL.md names each
// label as inline code and says they are written verbatim, at the start
// of a line; the form alone did not stop a model from rewording one.
func TestSkillStatesLabelsVerbatim(t *testing.T) {
	skill := skillFiles(t)[filepath.Join(skillDir, "SKILL.md")]
	prose := textBlock.ReplaceAllString(skill, "")
	for _, label := range skillLabels {
		if !strings.Contains(prose, "`"+label+"`") {
			t.Errorf("SKILL.md does not name the label `%s` in its rule", label)
		}
	}
	if !strings.Contains(prose, "verbatim") || !strings.Contains(prose, "start of a line") {
		t.Error("SKILL.md does not say the labels are written verbatim, at the start of a line")
	}
}

// TestSkillExamplesAreApprovable: every explanation in the skill's
// examples (a text block with a Commands: line) is one the trial's
// parser approves; the examples run on "the laptop".
func TestSkillExamplesAreApprovable(t *testing.T) {
	path := filepath.Join(skillDir, "references", "examples.md")
	n := 0
	for _, m := range textBlock.FindAllStringSubmatch(skillFiles(t)[path], -1) {
		if !strings.Contains(m[1], "\nCommands:\n") {
			continue
		}
		n++
		if ok, missing := approvable(m[1], []string{"laptop"}); !ok {
			t.Errorf("%s: explanation %q: missing %v", path, strings.SplitN(m[1], "\n", 2)[0], missing)
		}
	}
	if n == 0 {
		t.Errorf("%s: no explanation found", path)
	}
}

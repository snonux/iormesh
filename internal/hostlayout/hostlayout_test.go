package hostlayout

import (
	"os"
	"strings"
	"testing"
)

// TestDocsShowTheRule holds the documentation to the generated rule:
// security.md's example (user deploy) and testing.md's manual bootstrap
// (user ops) must spell exactly Rule, so the docs cannot drift from what
// bootstrap installs and the tracer's gate expects.
func TestDocsShowTheRule(t *testing.T) {
	for file, user := range map[string]string{"../../docs/security.md": "deploy", "../../docs/testing.md": "ops"} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), Rule(user)) {
			t.Errorf("%s does not show the rule %q", file, Rule(user))
		}
	}
}

func TestSudoersContent(t *testing.T) {
	want := SudoersMarker + "\nops ALL=(root) NOPASSWD: /opt/iormesh/bin/iormesh\n"
	if got := SudoersContent("ops"); got != want {
		t.Errorf("SudoersContent = %q, want %q", got, want)
	}
	// The printed steps put RuleSuffix in double quotes.
	if strings.ContainsAny(RuleSuffix, "\"$`\\!") {
		t.Errorf("RuleSuffix %q holds a character double quotes interpret", RuleSuffix)
	}
	if strings.ContainsAny(SudoersMarker, "'\n") {
		t.Errorf("SudoersMarker %q holds a quote or newline", SudoersMarker)
	}
}

func TestValidUser(t *testing.T) {
	for _, u := range []string{"ops", "deploy", "a.b-c_d", "_svc"} {
		if !ValidUser(u) {
			t.Errorf("ValidUser(%q) = false", u)
		}
	}
	for _, u := range []string{"", "-ops", "ops,root", "ops ALL", "%wheel", "a:b", "a=b", "#0", "a\nb",
		"ops\\", "\"ops\"", strings.Repeat("a", 33)} {
		if ValidUser(u) {
			t.Errorf("ValidUser(%q) = true", u)
		}
	}
}

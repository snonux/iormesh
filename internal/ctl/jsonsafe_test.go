package ctl

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"unicode"

	"iormesh/internal/expand"
	"iormesh/internal/report"
	"iormesh/internal/viewmodel"
)

// hostile is a traced string a compromised server could plant: a C1 CSI
// (U+009B) clearing the screen, a bidi override and DEL — none of which
// encoding/json escapes on its own.
const hostile = "x\u009b2J\u009b31m\u202eowned\x7f"

// assertTermSafe fails when out holds a rune a terminal could act on
// (anything unicode.IsPrint rejects, newlines aside) and checks that it
// still decodes to want: escaping must not change the value.
func assertTermSafe[T any](t *testing.T, name, out string, want T) {
	t.Helper()
	for _, r := range out {
		if r != '\n' && !unicode.IsPrint(r) {
			t.Errorf("%s: raw %U in the output:\n%s", name, r, out)
			break
		}
	}
	var got T
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%s: %v:\n%s", name, err, out)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: decodes to\n%+v\nwant\n%+v", name, got, want)
	}
}

// rewriteJSON decodes the JSON file at path into a T, lets edit change
// it and writes it back.
func rewriteJSON[T any](t *testing.T, path string, edit func(*T)) T {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	edit(&v)
	if data, err = json.Marshal(v); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestShowJSONTermSafe: a hostile label in the model and the report
// reaches `show sequence -json`, `show summary -json` and
// `show report -json` escaped (\u escapes), and each still decodes to
// the stored value.
func TestShowJSONTermSafe(t *testing.T) {
	f := newBuildFixture(t)
	f.mustRun(t, "build")
	dir := filepath.Join(f.builds, "billing+shop")
	m := rewriteJSON(t, filepath.Join(dir, modelFileName), func(m *viewmodel.Model) {
		m.Conversations = append([]viewmodel.Conversation{{ID: "0001", Entry: hostile, Label: hostile}},
			m.Conversations...)
	})
	assertTermSafe(t, "show sequence -json", f.mustRun(t, "show", "sequence", "-json"), m.Conversations[0])
	assertTermSafe(t, "show summary -json", f.mustRun(t, "show", "summary", "-json"), m)
	r := rewriteJSON(t, filepath.Join(dir, reportFileName), func(r *report.Report) {
		r.Processes = append(r.Processes, report.Process{Comm: hostile, Argv: []string{hostile}})
	})
	assertTermSafe(t, "show report -json", f.mustRun(t, "show", "report", "-json"), r)
}

// TestExpandJSONTermSafe: the expand plan's JSON escapes a hostile string
// the same way (writeProposal, what `expand -json` prints).
func TestExpandJSONTermSafe(t *testing.T) {
	p := expand.Proposal{Deploy: []expand.Candidate{{Rule: hostile, Projects: []string{hostile}}}}
	var out bytes.Buffer
	if err := writeProposal(env{stdout: &out}, p, true); err != nil {
		t.Fatal(err)
	}
	assertTermSafe(t, "expand -json", out.String(), p)
}

// TestShowJSONPlainUnchanged: printable text passes through untouched,
// so a JSON view without anything to escape keeps its exact bytes.
func TestShowJSONPlainUnchanged(t *testing.T) {
	p := expand.Proposal{Deploy: []expand.Candidate{{Rule: "cidr 10.0.0.0/8 – ünïcode", Projects: []string{"shop"}}}}
	var out bytes.Buffer
	if err := writeProposal(env{stdout: &out}, p, true); err != nil {
		t.Fatal(err)
	}
	want, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != string(want)+"\n" {
		t.Errorf("plain plan changed:\n%s\nwant\n%s", out.String(), want)
	}
}

package viewmodel

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode"
)

// TestRunQueryPasteIsInert (task 1i4): a folded run's query, pasted
// from show sequence into a shell, runs iormeshctl and nothing else,
// even when the traced path holds a non-printable rune and shell code.
// The query once went through termsafe.String, which Go-quoted the
// whole finished command and broke its single quotes, so pasting it ran
// the path's shell code. Each path's injected code writes a marker file
// with a shell builtin (a redirection), so nothing outside the temp dir
// is executed: PATH holds only the temp dir and its stub iormeshctl,
// which records its arguments. Each command is pasted as printed and
// with an outer pair of double quotes stripped, as an operator might.
func TestRunQueryPasteIsInert(t *testing.T) {
	for _, p := range []string{
		"/data/up/x';echo>PWNED;'x\x01",
		"/data/up/$(echo>PWNED)x\x01",
		"/data/up/`echo>PWNED`‮x\xff\x7f",
	} {
		line := sequenceRunLine(t, runQuery("h1.example", 21, p, base, base+5*ms))
		if strings.IndexFunc(line, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
			t.Errorf("path %q: the printed query holds a non-printable rune: %q", p, line)
		}
		for _, cmd := range []string{line, strings.TrimSuffix(strings.TrimPrefix(line, `"`), `"`)} {
			args := pasteIntoShell(t, cmd)
			if i := slices.Index(args, "-file"); i < 0 || i+1 >= len(args) {
				t.Errorf("path %q: stub got no -file: %q", p, args)
			} else if ok, err := path.Match(args[i+1], p); !ok || err != nil {
				t.Errorf("path %q: -file %q does not match it (%v)", p, args[i+1], err)
			}
		}
	}
}

// sequenceRunLine is the text after "run " that show sequence prints for
// a folded step whose query is q.
func sequenceRunLine(t *testing.T, q string) string {
	t.Helper()
	c := Conversation{ID: "c1", Steps: []Step{{Kind: "file", Count: 2, Ref: "p/h/s/1",
		LastRef: "p/h/s/2", RunQuery: q}}}
	var b strings.Builder
	if err := WriteSequence(&b, c); err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(b.String(), "\n") {
		if _, after, ok := strings.Cut(l, "    run "); ok {
			return after
		}
	}
	t.Fatalf("show sequence printed no run line:\n%s", b.String())
	return ""
}

// pasteIntoShell runs cmd with sh -c in a fresh temp dir whose stub
// iormeshctl is the only command on PATH, fails the test when the
// command left a marker file there, and returns the stub's arguments.
func pasteIntoShell(t *testing.T, cmd string) []string {
	t.Helper()
	dir := t.TempDir()
	stub := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\0' \"$a\"; done > \"$PWD/args\"\n"
	if err := os.WriteFile(filepath.Join(dir, "iormeshctl"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	sh := exec.Command("/bin/sh", "-c", cmd)
	sh.Dir, sh.Env = dir, []string{"PATH=" + dir}
	out, err := sh.CombinedOutput()
	if _, serr := os.Stat(filepath.Join(dir, "PWNED")); serr == nil {
		t.Errorf("pasting %q ran shell code from the path (output %q)", cmd, out)
	}
	if err != nil {
		t.Logf("pasting %q: %v: %s", cmd, err, out)
	}
	data, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
}

// TestGlobPrintable (task pp4): GlobPrintable turns each non-printable
// rune (C0, C1, bidi) and each invalid byte into '?' and keeps a glob's
// metacharacters, where GlobLiteral escapes them to match only
// themselves.
func TestGlobPrintable(t *testing.T) {
	for _, tc := range []struct{ in, printable, literal string }{
		{"a\x1bb", "a?b", "a?b"},
		{"a\u009bb", "a?b", "a?b"},
		{"a‮b", "a?b", "a?b"},
		{"a\xffb", "a?b", "a?b"},
		{"[prod]/*x*", "[prod]/*x*", `\[prod\]/\*x\*`},
		{"plain/path", "plain/path", "plain/path"},
	} {
		if got := GlobPrintable(tc.in); got != tc.printable {
			t.Errorf("GlobPrintable(%q) = %q, want %q", tc.in, got, tc.printable)
		}
		if got := GlobLiteral(tc.in); got != tc.literal {
			t.Errorf("GlobLiteral(%q) = %q, want %q", tc.in, got, tc.literal)
		}
	}
}

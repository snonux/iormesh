package ctl

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode"

	"iormesh/internal/schema"
)

// TestQueryFileBaseHintPasteIsInert (task pp4): the -file glob the hint
// suggests (decision #41) is built from a traced path, which a server
// chooses, and is meant to be pasted after `iormeshctl query`, often by
// an agent, verbatim. Pasted into a shell it must run iormeshctl and
// nothing else, as exactly one -file word that matches the path through
// path.Match, the matcher query's -file uses: a single quote in the
// directory once closed the hint's own quotes and ran the rest, and a
// glob metacharacter there ([prod]) once made the suggested glob miss
// the path. The hint stays printable with a non-printable rune (C0, C1,
// bidi) in the path. An unclean path (//, ./, ..) gets a glob of its
// directory as traced, not cleaned, so the glob still matches it. An
// ordinary path keeps its plain form.
func TestQueryFileBaseHintPasteIsInert(t *testing.T) {
	for _, tc := range []struct{ path, glob, want string }{
		{"/tmp/a';echo>PWNED;'/billing.crt", "*billing.crt*", ""},
		{"/data/[prod]/billing.crt", "*billing.crt*", `try -file '/data/\[prod\]/*billing.crt*'`},
		{"/var/x\x01y/billing.crt", "*billing.crt*", ""},
		{"/var/$(echo>PWNED)/billing.crt", "*billing.crt*", ""},
		{"/var/x\u009by/billing.crt", "*billing.crt*", ""},
		{"/var/x‮y/billing.crt", "*billing.crt*", ""},
		{"/a//b/billing.crt", "*billing.crt*", "try -file '/a//b/*billing.crt*'"},
		{"/a/./b/billing.crt", "*billing.crt*", ""},
		{"a/../b/billing.crt", "*billing.crt*", "try -file 'a/../b/*billing.crt*'"},
		{"/etc/billing.crt", "*billing.crt*", "try -file '/etc/*billing.crt*'"},
	} {
		text := fileBaseHintText(t, tc.path, tc.glob)
		if tc.want != "" && text != strings.TrimPrefix(tc.want, "try ") {
			t.Errorf("path %q: hint suggests %q, want %q", tc.path, text, tc.want)
		}
		if strings.IndexFunc(text, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
			t.Errorf("path %q: the hint holds a non-printable rune: %q", tc.path, text)
		}
		args := pasteQuery(t, "iormeshctl query "+text)
		files := 0
		for i, a := range args {
			if a != "-file" || i+1 >= len(args) {
				continue
			}
			files++
			if ok, err := path.Match(args[i+1], tc.path); !ok || err != nil {
				t.Errorf("path %q: suggested -file %q does not match it (%v)", tc.path, args[i+1], err)
			}
		}
		if files != 1 || len(args) != 3 {
			t.Errorf("path %q: pasting %q gave iormeshctl %q, want query -file <glob>", tc.path, text, args)
		}
	}
}

// fileBaseHintText runs a query with -file glob over a store whose one
// file event opens p, and returns the text after "try " in the hint the
// query printed on stderr.
func fileBaseHintText(t *testing.T, p, glob string) string {
	t.Helper()
	f := newBuildFixture(t)
	h, methods := javaTrace()
	for i := range h.events {
		if h.events[i].Event == schema.KindOpenat {
			h.events[i].Args.Path = bptr(p)
		}
	}
	writeJavaSegment(t, f.store, "appJ", h, methods)
	f.mustRun(t, slices.Concat([]string{"query"}, queryProjects, []string{"-file", glob})...)
	stderr := f.stderr.String()
	_, after, ok := strings.Cut(stderr, "; try ")
	if !ok {
		t.Fatalf("path %q: no hint on stderr: %s", p, stderr)
	}
	line, _, _ := strings.Cut(after, "\n")
	return line
}

// pasteQuery runs cmd with sh -c in a fresh temp dir whose stub
// iormeshctl, recording its arguments, is the only command on PATH
// (as viewmodel's TestRunQueryPasteIsInert does); it fails the test when
// the command left the marker file PWNED there, which the planted
// paths write with a shell builtin, and returns the stub's arguments.
func pasteQuery(t *testing.T, cmd string) []string {
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

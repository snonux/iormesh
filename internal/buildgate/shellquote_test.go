package buildgate

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// shellQuoteDirs are the packages that print commands for an operator or
// agent to paste: iormeshctl's commands, the TUI and the view model
// they render, and the expansion hints.
var shellQuoteDirs = []string{"internal/ctl", "internal/tui", "internal/viewmodel", "internal/expand"}

// quotedVerb is a %s or %v verb, flags and width included, between single
// quotes: a hand-made shell quote around a formatted value.
var quotedVerb = regexp.MustCompile(`'%[-+# 0-9.*]*[sv]'`)

// TestNoHandQuotedVerbs (task pp4) fails on a string literal in a
// non-test file of shellQuoteDirs holding '%s' or '%v'. A value
// formatted between single quotes is a shell word only while it holds
// no single quote: query's -file hint printed a traced path that way,
// and a path with a quote in it made the paste-ready command run shell
// code. A printed command quotes its words with viewmodel.ShellWord
// (tasks 1i4, 3i4) and prints them with a bare %s. Text that merely
// names a value in quotes trips the gate too; write it with %q, or
// without the quotes.
func TestNoHandQuotedVerbs(t *testing.T) {
	root := repoRoot(t)
	var got []string
	seen := 0
	for _, dir := range shellQuoteDirs {
		names, err := filepath.Glob(filepath.Join(root, dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			seen++
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, name, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, handQuotedVerbs(fset, f, dir)...)
		}
	}
	if seen == 0 {
		t.Fatalf("no Go file found under %v", shellQuoteDirs)
	}
	for _, g := range got {
		t.Error(g)
	}
}

// TestHandQuotedVerbGateCatches (negative): the gate flags '%s', '%v'
// and '%-10s' in literals, raw strings included, and passes %q, a bare
// %s and a quote that is not around a verb.
func TestHandQuotedVerbGateCatches(t *testing.T) {
	const src = "package ctl\n\nimport \"fmt\"\n\n" +
		"func f(p string) {\n" +
		"\tfmt.Printf(\"try -file '%s'\\n\", p)\n" +
		"\tfmt.Printf(`ref '%v'`, p)\n" +
		"\tfmt.Printf(\"%q '%-10s'\", p, p)\n" +
		"\tfmt.Printf(\"%q %s '*' stops at '/'\", p, p)\n" +
		"}\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := handQuotedVerbs(fset, f, "internal/ctl")
	want := []string{
		`internal/ctl/fixture.go:6: '%s' in a literal; quote a printed word with viewmodel.ShellWord`,
		`internal/ctl/fixture.go:7: '%v' in a literal; quote a printed word with viewmodel.ShellWord`,
		`internal/ctl/fixture.go:8: '%-10s' in a literal; quote a printed word with viewmodel.ShellWord`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("findings\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// handQuotedVerbs lists, as "dir/file.go:line: message", each string
// literal in f holding a single-quoted %s or %v verb.
func handQuotedVerbs(fset *token.FileSet, f *ast.File, dir string) []string {
	var got []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		for _, m := range quotedVerb.FindAllString(lit.Value, -1) {
			pos := fset.Position(lit.Pos())
			got = append(got, fmt.Sprintf("%s/%s:%d: %s in a literal; quote a printed word with viewmodel.ShellWord",
				dir, filepath.Base(pos.Filename), pos.Line, m))
		}
		return true
	})
	return got
}

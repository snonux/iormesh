package schema

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestKindsListsEveryKind: Kinds holds exactly the string values of the
// package's Kind* constants, sorted, each one a KnownKind — so a kind
// added to a kinds_*.go file without Kinds' list fails here, and query
// -event (task wh4) never refuses a kind the schema accepts.
func TestKindsListsEveryKind(t *testing.T) {
	declared := kindConstants(t)
	got := Kinds()
	if !slices.IsSorted(got) {
		t.Errorf("Kinds is not sorted: %v", got)
	}
	if !slices.Equal(got, declared) {
		t.Errorf("Kinds %v\nwant the Kind* constants %v", got, declared)
	}
	for _, k := range got {
		if !KnownKind(k) {
			t.Errorf("%s is listed but not a KnownKind", k)
		}
	}
	got[0] = "mutated"
	if Kinds()[0] == "mutated" {
		t.Error("Kinds returned its own slice")
	}
}

// kindConstants parses the package's non-test sources and returns the
// values of its Kind* string constants, sorted.
func kindConstants(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, fileKindConstants(t, f)...)
	}
	slices.Sort(out)
	return out
}

// fileKindConstants returns the values of f's Kind* string constants.
func fileKindConstants(t *testing.T, f *ast.File) []string {
	t.Helper()
	var out []string
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, s := range g.Specs {
			v := s.(*ast.ValueSpec)
			for i, n := range v.Names {
				if !strings.HasPrefix(n.Name, "Kind") || i >= len(v.Values) {
					continue
				}
				lit, ok := v.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				out = append(out, s)
			}
		}
	}
	return out
}

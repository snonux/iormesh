package buildgate

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The hand-written BPF C under internal/c was lifted from ior, and its
// comments kept citing ior's Go files and tests (internal/bpfsetup_kfunc.go,
// TestDefaultEventMapSizeMatchesMapsH, ...) that iormesh does not have, so a
// reader was told a behaviour was handled or pinned when nothing here did it
// (task gj4). Go code would not compile against a missing name; a comment
// does, so this gate is the compiler for the references comments make.

var (
	// cCommentRE matches a // line comment or a /* */ block comment. String
	// literals in the BPF C never contain "//" or "/*", so no lexer is needed.
	cCommentRE = regexp.MustCompile(`(?s)//[^\n]*|/\*.*?\*/`)
	// cRefRE matches what a comment may claim exists in this repository: an
	// internal/... path or a Go test function. The optional "ior " or
	// "ior's " prefix is captured so a reference into ior's repository (a
	// provenance header, "ior internal/c/restart.c") can be told apart.
	cRefRE = regexp.MustCompile(`(ior(?:'s)? +)?\b(internal/[A-Za-z0-9_./-]*[A-Za-z0-9_]|Test[A-Z][A-Za-z0-9_]*)`)
	// goTestFuncRE matches a top-level test function declaration.
	goTestFuncRE = regexp.MustCompile(`(?m)^func (Test[A-Z][A-Za-z0-9_]*)\(`)
)

// staleCRefs returns the references in the comments of C source src that
// do not resolve: an internal/... path that is neither a file nor a
// directory under root, or a Test* name not in tests. References prefixed
// with "ior" name ior's repository and are not checked. Comment lines are
// joined first, so "ior" at the end of one line still exempts a path that
// wraps onto the next.
func staleCRefs(src, root string, tests map[string]bool) []string {
	var stale []string
	for _, c := range cCommentRE.FindAllString(src, -1) {
		text := strings.Join(strings.Fields(strings.NewReplacer(
			"//", " ", "/*", " ", "*/", " ", "*", " ").Replace(c)), " ")
		for _, m := range cRefRE.FindAllStringSubmatch(text, -1) {
			if m[1] != "" || slices.Contains(stale, m[2]) {
				continue
			}
			ref := m[2]
			if strings.HasPrefix(ref, "Test") {
				if !tests[ref] {
					stale = append(stale, ref)
				}
				continue
			}
			if _, err := os.Stat(filepath.Join(root, ref)); err != nil {
				stale = append(stale, ref)
			}
		}
	}
	return stale
}

// goTestFuncs returns the name of every Test* function in the module's
// _test.go files, so a comment may cite a test from any package.
func goTestFuncs(t *testing.T, root string) map[string]bool {
	t.Helper()
	tests := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "vendor" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range goTestFuncRE.FindAllStringSubmatch(string(b), -1) {
			tests[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return tests
}

// TestCCommentsNameExistingCode fails when a comment in internal/c/*.{c,h}
// names an internal/... path or a Test* function this repository does not
// have. vmlinux.h is generated from kernel BTF and carries no such comments.
func TestCCommentsNameExistingCode(t *testing.T) {
	root := repoRoot(t)
	tests := goTestFuncs(t, root)
	files, err := filepath.Glob(filepath.Join(root, "internal", "c", "*.[ch]"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no C sources under internal/c (err %v)", err)
	}
	for _, f := range files {
		if filepath.Base(f) == "vmlinux.h" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, ref := range staleCRefs(string(b), root, tests) {
			t.Errorf("%s: comment names %s, which does not exist in iormesh", filepath.Base(f), ref)
		}
	}
}

// TestStaleCRefsFlagsMissingNames pins the scanner itself: a missing path
// and a missing test are flagged, while a real path, a real test and a
// reference into ior's repository (also wrapped across lines) are not.
func TestStaleCRefsFlagsMissingNames(t *testing.T) {
	root := repoRoot(t)
	tests := map[string]bool{"TestReal": true}
	src := "// Adapted from ior internal/nope.go @ abc.\n" +
		"int x; // see internal/c/maps.h and TestReal\n" +
		"/* the fold (ior\n * internal/c/restart.c) */\n" +
		"// lost: internal/missing_file.go, pinned by TestGone.\n"
	got := staleCRefs(src, root, tests)
	want := []string{"internal/missing_file.go", "TestGone"}
	if !slices.Equal(got, want) {
		t.Fatalf("staleCRefs = %q, want %q", got, want)
	}
}

package buildgate

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// docLineLimit is decision #36's exact size limit: a Markdown file in the
// repository has at most this many lines; past it, the file becomes an
// index plus a folder of parts. readmeLineLimit is README.md's tighter
// bound, the "README stays lean" rule (AGENTS.md) made a number.
const (
	docLineLimit    = 600
	readmeLineLimit = 150
)

// docSizeAllowed are the reviewed exemptions, a slash-separated path from
// the repository root to why that file may pass the limit. Empty today:
// decision #36 names none. An entry whose file is no longer over the limit
// fails, so the table cannot keep a file unwatched after it shrinks.
var docSizeAllowed = map[string]string{}

// TestDocsUnderSizeLimit (task sm4) enforces decision #36: it fails on
// every Markdown file of the repository over its line limit, naming the
// file, its count and the remedy. Without it the rule broke in the very
// next commit after it landed (decided.md grew to 710 lines).
func TestDocsUnderSizeLimit(t *testing.T) {
	problems, err := oversizeDocs(repoRoot(t), docSizeAllowed)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// TestDocSizeGateCatchesOversize (negative): a planted file one line over
// the limit is reported, one at the limit passes, README.md has its own
// lower bound, files in dot, bin, vendor and node_modules directories are
// skipped, an exemption silences its file and a stale exemption fails.
func TestDocSizeGateCatchesOversize(t *testing.T) {
	root := t.TempDir()
	plant(t, root, "docs/at.md", docLineLimit)
	plant(t, root, "docs/deep/over.md", docLineLimit+1)
	plant(t, root, "docs/exempt.md", docLineLimit+5)
	plant(t, root, "README.md", readmeLineLimit+1)
	plant(t, root, "AGENTS.md", readmeLineLimit+1)
	plant(t, root, "notes.txt", docLineLimit+1)
	for _, dir := range []string{".claude", "bin", "vendor", "node_modules"} {
		plant(t, root, dir+"/big.md", docLineLimit+1)
	}
	allowed := map[string]string{
		"docs/exempt.md": "planted",
		"docs/at.md":     "stale: under the limit",
	}
	problems, err := oversizeDocs(root, allowed)
	if err != nil {
		t.Fatal(err)
	}
	wantPrefixes := []string{
		"README.md has 151 lines",
		"docs/deep/over.md has 601 lines",
		`docSizeAllowed entry "docs/at.md"`,
	}
	if len(problems) != len(wantPrefixes) {
		t.Fatalf("got %d problems, want %d:\n%s", len(problems), len(wantPrefixes), strings.Join(problems, "\n"))
	}
	for i, want := range wantPrefixes {
		if !strings.HasPrefix(problems[i], want) {
			t.Errorf("problem %d = %q, want prefix %q", i, problems[i], want)
		}
	}
}

// plant writes a file of exactly n lines under root, creating its directory.
func plant(t *testing.T, root, rel string, n int) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("line\n", n)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// oversizeDocs walks root for *.md files and returns one message per file
// over its limit and not in allowed, then one per stale allowed entry,
// sorted so the output is stable. Directories starting with a dot (.git,
// and .claude, which holds other worktrees), bin (build outputs, decision
// #30), vendor and node_modules are not the repository's docs and are
// skipped.
func oversizeDocs(root string, allowed map[string]string) ([]string, error) {
	var over, stale []string
	used := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skipDocDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		n, limit, err := docLines(path, rel)
		if err != nil || n <= limit {
			return err
		}
		if _, ok := allowed[rel]; ok {
			used[rel] = true
			return nil
		}
		over = append(over, fmt.Sprintf("%s has %d lines, over the limit of %d (docs/decisions.md #36): "+
			"keep its path as an index and move its sections into a folder of parts", rel, n, limit))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	for rel := range allowed {
		if !used[rel] {
			stale = append(stale, fmt.Sprintf("docSizeAllowed entry %q is not a file over the limit; drop it", rel))
		}
	}
	slices.Sort(over)
	slices.Sort(stale)
	return append(over, stale...), nil
}

// skipDocDir reports whether a directory holds no repository docs.
func skipDocDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "bin" || name == "vendor" || name == "node_modules"
}

// docLines returns the file's line count, counted as wc -l does plus a
// final line without a newline, and the limit that applies to it.
func docLines(path, rel string) (int, int, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	n := bytes.Count(src, []byte("\n"))
	if len(src) > 0 && src[len(src)-1] != '\n' {
		n++
	}
	limit := docLineLimit
	if rel == "README.md" {
		limit = readmeLineLimit
	}
	return n, limit, nil
}

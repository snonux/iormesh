//go:build mage

// Adapted from ior Magefile.go @ b21afc50328c (Fmt, FmtCheck, fmtGoFiles).
// Changes: the walk callback is split into helpers under 50 lines; the
// rewrite no longer passes the always-zero DirEntry permission bits.

package main

import (
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Fmt runs gofmt -w on all Go source files to enforce canonical formatting.
func Fmt() error {
	return fmtGoFiles(false)
}

// FmtCheck verifies that all Go source files are gofmt-formatted and fails
// listing those that are not, so it can gate CI.
func FmtCheck() error {
	return fmtGoFiles(true)
}

// fmtWalk collects what a formatting walk found.
type fmtWalk struct {
	checkOnly   bool
	unformatted []string
	broken      []string
}

// fmtGoFiles walks the repo tree and either formats or checks every .go file.
func fmtGoFiles(checkOnly bool) error {
	w := &fmtWalk{checkOnly: checkOnly}
	if err := filepath.WalkDir(".", w.visit); err != nil {
		return err
	}
	if len(w.broken) > 0 {
		return fmt.Errorf("gofmt: %d file(s) have syntax errors:\n  %s",
			len(w.broken), strings.Join(w.broken, "\n  "))
	}
	if len(w.unformatted) > 0 {
		return fmt.Errorf("gofmt: %d file(s) need formatting:\n  %s\nRun `mage fmt` to fix",
			len(w.unformatted), strings.Join(w.unformatted, "\n  "))
	}
	return nil
}

func (w *fmtWalk) visit(path string, d fs.DirEntry, err error) error {
	if err != nil {
		return err
	}
	// Skip vendor and hidden directories (e.g. .git) - but not the walk root
	// itself: its name is ".", and skipping it once made both targets
	// silent no-ops in ior.
	if d.IsDir() && path != "." && (d.Name() == "vendor" || strings.HasPrefix(d.Name(), ".")) {
		return filepath.SkipDir
	}
	if d.IsDir() || !strings.HasSuffix(path, ".go") {
		return nil
	}
	return w.formatFile(path)
}

func (w *fmtWalk) formatFile(path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	formatted, err := format.Source(src)
	if err != nil {
		// Keep walking so every unparseable file is listed, but fail the
		// target afterwards: such a file is neither formatted nor checked.
		fmt.Printf("gofmt: syntax error in %s: %v\n", path, err)
		w.broken = append(w.broken, path)
		return nil
	}
	if string(src) == string(formatted) {
		return nil
	}
	if w.checkOnly {
		w.unformatted = append(w.unformatted, path)
		return nil
	}
	// The file exists, so WriteFile keeps its mode; ior passed
	// DirEntry.Type().Perm(), which is always 0 and only looked meaningful.
	return os.WriteFile(path, formatted, 0o644)
}

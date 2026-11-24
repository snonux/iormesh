// Package site writes the local explorer of a consolidation run
// (docs/milestones.md M5 "Interactive explorer", decision #11;
// docs/visualization.md "The local site", step V3): a folder of static files
// that opens from file:// — no server, no dependency, no network request.
//
//	<builds>/<projects>/sites/
//	  latest -> 20251120T005320Z-3f9c0a1b2c4d/
//	  20251120T005320Z-3f9c0a1b2c4d/
//	    index.html          the explorer: CSS, script and model inline
//	    graph.svg           the deterministic exports (internal/render),
//	    sequence/<ID>.svg   linked from the page
//
// One folder per run, kept until the operator deletes it: a build never
// removes an older run's folder, so two runs can be opened side by side.
// The folder is named after the run's last traced instant (the model's
// Run.Last — a trace time, never the build machine's clock) and a hash
// of its files, so a rebuild over the same store writes the same folder
// with the same bytes, and any change of data gives a new one.
//
// The page renders the view model only (index.html embeds it), the same
// model `show` and the TUI read, so the three surfaces cannot disagree.
// Every string in it came from the traced servers; page.go and
// assets/app.js keep it data — see Page.
//
// It is cgo-free, like everything iormeshctl links.
package site

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"iormesh/internal/render"
	"iormesh/internal/schema"
	"iormesh/internal/viewmodel"
)

const (
	// LatestName is the symlink to the newest run's folder.
	LatestName = "latest"
	// IndexName is the explorer page in a run's folder.
	IndexName = "index.html"
	// runTime formats Run.Last in a folder name: sortable, no colon.
	runTime = "20060102T150405Z"
	// hashLen is the hex digits of the files' hash in a folder name.
	hashLen = 12
)

// File is one file of a run's site: its path in the folder ("/"
// separated) and its bytes.
type File struct {
	Path string
	Data []byte
}

// Files renders a run's site: index.html, graph.svg and one
// sequence/<ID>.svg per conversation, in that order. A conversation ID
// is a file name here, so one that is not all digits (the model numbers
// them "0001", …) is refused rather than trusted to stay in the folder —
// as app.js links only such IDs to their SVG.
func Files(g schema.Graph, m viewmodel.Model) ([]File, error) {
	page, err := Page(m)
	if err != nil {
		return nil, err
	}
	files := []File{{IndexName, page}, {"graph.svg", render.Graph(g, render.NamesOf(m))}}
	for _, c := range m.Conversations {
		if !digits(c.ID) {
			return nil, fmt.Errorf("site: conversation ID %q is not a number", c.ID)
		}
		files = append(files, File{"sequence/" + c.ID + ".svg", render.Sequence(c)})
	}
	return files, nil
}

// RunName names a run's folder: Run.Last, then a hash of the files.
func RunName(m viewmodel.Model, files []File) string {
	h := sha256.New()
	for _, f := range files {
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00", f.Path, len(f.Data)) // a hash never fails to write
		h.Write(f.Data)
	}
	return m.Run.Last.UTC().Format(runTime) + "-" + hex.EncodeToString(h.Sum(nil))[:hashLen]
}

// Write writes a run's site into a new folder under root, points
// root/latest at it and returns the folder. The folder is written whole
// under a temporary name and renamed into place, so a half-written run
// is never opened; root and every folder are 0700 and files 0600 — the
// model names hosts, processes and files of production servers.
func Write(root string, g schema.Graph, m viewmodel.Model) (string, error) {
	files, err := Files(g, m)
	if err != nil {
		return "", err
	}
	if err := ownDir(root); err != nil {
		return "", err
	}
	name := RunName(m, files)
	tmp, err := os.MkdirTemp(root, ".run-*")
	if err != nil {
		return "", fmt.Errorf("site: %w", err)
	}
	if err := writeFiles(tmp, files); err != nil {
		return "", errors.Join(err, os.RemoveAll(tmp))
	}
	final := filepath.Join(root, name)
	// Same name, same content: a folder already there is this run's,
	// replaced whole so a file the operator removed from it comes back.
	if err := os.RemoveAll(final); err != nil {
		return "", errors.Join(fmt.Errorf("site: %w", err), os.RemoveAll(tmp))
	}
	if err := os.Rename(tmp, final); err != nil {
		return "", errors.Join(fmt.Errorf("site: %w", err), os.RemoveAll(tmp))
	}
	return final, pointLatest(root, name)
}

// Latest is the index.html of root's newest run, through the latest
// link; the link must name a real folder (not another symlink) directly
// under root, holding a regular index.html.
func Latest(root string) (string, error) {
	target, err := os.Readlink(filepath.Join(root, LatestName))
	if err != nil {
		return "", fmt.Errorf("site: %w", err)
	}
	if target == "" || target == "." || target == ".." || strings.ContainsRune(target, filepath.Separator) {
		return "", fmt.Errorf("site: %s points outside %s: %q", LatestName, root, target)
	}
	if fi, err := os.Lstat(filepath.Join(root, target)); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("site: %s does not name a run folder in %s: %q", LatestName, root, target)
	}
	index := filepath.Join(root, target, IndexName)
	if fi, err := os.Lstat(index); err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("site: %s is not a regular file", index)
	}
	return index, nil
}

// digits says s is a non-empty string of ASCII digits.
func digits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// ownDir makes dir (0700) and refuses one that is not a real directory:
// a symlink there would send the site elsewhere.
func ownDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("site: %w", err)
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("site: %s is not a directory (a symlink?)", dir)
	}
	return os.Chmod(dir, 0o700)
}

// writeFiles writes files under dir, making their folders.
func writeFiles(dir string, files []File) error {
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return fmt.Errorf("site: %w", err)
		}
		if err := os.WriteFile(p, f.Data, 0o600); err != nil {
			return fmt.Errorf("site: %w", err)
		}
	}
	return nil
}

// pointLatest swaps root/latest to name: a new link under a temporary
// name, renamed over the old one, so latest always points at a whole
// run.
func pointLatest(root, name string) error {
	tmp := filepath.Join(root, "."+LatestName+".tmp")
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("site: %w", err)
	}
	if err := os.Symlink(name, tmp); err != nil {
		return fmt.Errorf("site: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(root, LatestName)); err != nil {
		return errors.Join(fmt.Errorf("site: %w", err), os.Remove(tmp))
	}
	return nil
}

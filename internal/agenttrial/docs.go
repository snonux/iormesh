package agenttrial

import (
	"io/fs"
	"os"
	"path/filepath"
)

// LibraryPaths are what the agent gets of the repository (decision #37,
// "What the agent gets"): README.md, the guide and the skill, at the
// commit under test. Links out of the guide into the topic docs are not
// copied: a run that needs one has found a gap in the guide.
var LibraryPaths = []string{"README.md", "docs/guide", "docs/skills/iormesh"}

// SkillFile is the skill's entry point inside the library.
const SkillFile = "docs/skills/iormesh/SKILL.md"

// CopyLibrary copies LibraryPaths from repo into dst, keeping their
// repository-relative paths so the skill's and the guide's relative
// links resolve inside the copy. Only regular files and directories are
// copied; a symlink is refused rather than followed out of the tree.
func CopyLibrary(repo, dst string) error {
	for _, p := range LibraryPaths {
		src := filepath.Join(repo, filepath.FromSlash(p))
		err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(repo, path)
			if err != nil {
				return err
			}
			return copyEntry(path, filepath.Join(dst, rel), d)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// copyEntry copies one walked entry: a directory is made, a regular file
// copied, anything else refused.
func copyEntry(src, dst string, d fs.DirEntry) error {
	switch {
	case d.IsDir():
		return os.MkdirAll(dst, 0o755)
	case !d.Type().IsRegular():
		return &fs.PathError{Op: "copy", Path: src, Err: fs.ErrInvalid}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

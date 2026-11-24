// Package buildenv resolves the build inputs the Mage targets take from
// outside the repository, so the resolution is ordinary, unit-tested Go
// rather than logic hidden behind the magefiles' `//go:build mage` tag.
package buildenv

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// LibbpfgoEnv is the variable that overrides the libbpfgo checkout location.
const LibbpfgoEnv = "LIBBPFGO"

// GitRunner runs git with args in dir and returns its trimmed stdout.
type GitRunner func(dir string, args ...string) (string, error)

// ExecGit is the GitRunner that runs the real git binary.
func ExecGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// LibbpfgoPath returns the libbpfgo checkout the build uses: override (the
// value of $LIBBPFGO) when set, else ../libbpfgo next to the main checkout of
// the repository whose working tree is root.
//
// In a plain checkout the main checkout is root itself. In a linked worktree
// (`git worktree add`, e.g. .claude/worktrees/agent-*) ../libbpfgo relative
// to root would point inside the main checkout, where no libbpfgo lives, so
// the main checkout is taken from git's common directory instead.
func LibbpfgoPath(override, root string, git GitRunner) string {
	if override != "" {
		return override
	}
	return filepath.Clean(filepath.Join(MainCheckout(root, git), "..", "libbpfgo"))
}

// MainCheckout returns the main working tree of the repository whose
// working tree is root: the parent of `git rev-parse --git-common-dir`,
// which every linked worktree shares with the main checkout. It falls back
// to root when git fails (no git, or a source tarball) or when the common
// directory is not a .git directory inside a working tree (a bare
// repository), so those layouts keep the plain ../libbpfgo behaviour.
//
// --path-format=absolute is avoided because it needs git 2.31; git prints a
// path relative to dir in the main checkout, so it is resolved against root.
func MainCheckout(root string, git GitRunner) string {
	common, err := git(root, "rev-parse", "--git-common-dir")
	if err != nil || common == "" {
		return root
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(root, common)
	}
	common = filepath.Clean(common)
	if filepath.Base(common) != ".git" {
		return root
	}
	return filepath.Dir(common)
}

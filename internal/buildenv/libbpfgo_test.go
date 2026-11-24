package buildenv

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGit answers `git rev-parse --git-common-dir` with out/err and records
// the directory it was asked about.
func fakeGit(t *testing.T, wantDir, out string, err error) GitRunner {
	t.Helper()
	return func(dir string, args ...string) (string, error) {
		if dir != wantDir {
			t.Errorf("git ran in %q, want %q", dir, wantDir)
		}
		if strings.Join(args, " ") != "rev-parse --git-common-dir" {
			t.Errorf("git args = %q", args)
		}
		return out, err
	}
}

func TestLibbpfgoPath(t *testing.T) {
	const main = "/src/iormesh"
	const wt = main + "/.claude/worktrees/agent-1"
	for _, tc := range []struct {
		name     string
		override string
		root     string
		out      string
		err      error
		want     string
	}{
		// A linked worktree reports the main checkout's absolute .git, so
		// libbpfgo is the main checkout's sibling, not the worktree's.
		{name: "linked worktree", root: wt, out: main + "/.git",
			want: "/src/libbpfgo"},
		// git prints a relative .git in the main checkout itself.
		{name: "plain checkout", root: main, out: ".git",
			want: "/src/libbpfgo"},
		{name: "override wins in a worktree", override: "/opt/libbpfgo",
			root: wt, out: main + "/.git", want: "/opt/libbpfgo"},
		// No git (a source tarball): fall back to the working tree.
		{name: "git fails", root: main, err: errors.New("not a git repo"),
			want: "/src/libbpfgo"},
		// A bare or unusual common dir names no main working tree.
		{name: "bare common dir", root: wt, out: "/srv/iormesh.git",
			want: main + "/.claude/worktrees/libbpfgo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			git := fakeGit(t, tc.root, tc.out, tc.err)
			if tc.override != "" {
				git = func(string, ...string) (string, error) {
					t.Error("git ran although the override is set")
					return "", nil
				}
			}
			got := LibbpfgoPath(tc.override, tc.root, git)
			if got != tc.want {
				t.Errorf("LibbpfgoPath = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestLibbpfgoPathRealWorktree checks the layout against a real
// `git worktree add`, so a change in what git prints is caught too.
func TestLibbpfgoPathRealWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(base, "iormesh")
	wt := filepath.Join(main, ".claude", "worktrees", "agent-1")
	runGit(t, base, "init", "-q", main)
	runGit(t, main, "-c", "user.name=t", "-c", "user.email=t@t",
		"commit", "-q", "--allow-empty", "-m", "init")
	runGit(t, main, "worktree", "add", "-q", wt)

	want := filepath.Join(base, "libbpfgo")
	for _, root := range []string{main, wt} {
		if got := LibbpfgoPath("", root, ExecGit); got != want {
			t.Errorf("LibbpfgoPath(%s) = %q, want %q", root, got, want)
		}
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Hermetic: neither the system nor the user's global config (e.g.
	// commit.gpgsign) may change what these commands do.
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

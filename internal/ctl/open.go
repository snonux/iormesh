package ctl

import (
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"iormesh/internal/site"
)

// `iormeshctl open [-project P]...` (PLAN.md M5 "Interactive explorer",
// decision #11): open the newest local site of the projects' build in
// the desktop's browser. It prints the page's path first, always — over
// SSH, or on a host without a desktop opener, that path is the whole
// answer (copy the folder, open it locally). open reads only the build
// output and starts no server.

// errNoOpener says no desktop opener is installed: the path printed is
// the result, not a failure.
var errNoOpener = errors.New("no opener")

// openCommand resolves the site and hands it to the opener.
func openCommand(args []string, e env) error {
	fs := newFlagSet("open", e.stderr)
	var f buildFlags
	addReadBuildFlags(fs, &f)
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: open takes no arguments", errUsage)
	}
	index, err := latestSite(e, f)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(e.stdout, "site       %s\n", safe(index)); err != nil {
		return err
	}
	opener := e.opener
	if opener == nil {
		opener = func(string) error { return errNoOpener }
	}
	err = opener(index)
	if errors.Is(err, errNoOpener) {
		_, err = fmt.Fprintln(e.stdout, "no desktop opener (xdg-open, open) found: open the path above in a browser")
	}
	return err
}

// latestSite is the index.html the sites/latest link of f's projects'
// build points at.
func latestSite(e env, f buildFlags) (string, error) {
	p, err := resolveShowPaths(e, f)
	if err != nil {
		return "", err
	}
	projects, err := buildProjects(p.store, f.projects)
	if err != nil {
		return "", err
	}
	root := filepath.Join(p.builds, buildKey(projects), sitesDirName)
	index, err := site.Latest(root)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("open: no site of %s yet (iormeshctl build)", strings.Join(projects, "+"))
	}
	if err != nil {
		return "", fmt.Errorf("open: %w", err)
	}
	return index, nil
}

// systemOpener starts the desktop's opener on path (xdg-open on Linux
// and the BSDs, open on macOS) without waiting for the browser; the
// path is absolute, so it cannot read as an option.
func systemOpener(path string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	bin, err := exec.LookPath(name)
	if err != nil {
		return errNoOpener
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, abs)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open: %s: %w", name, err)
	}
	return cmd.Process.Release()
}

package integrationtests

// M2 steps 4–5 end to end: the file surface's scenarios under the real
// tracer pass their goldens (docs/milestones.md M2 "Full file surface";
// docs/implementation.md M2 steps 4–5), including the rename-aware path of
// an fd that stays open across a rename.

import (
	"slices"
	"testing"

	"iormesh/internal/normalize"
	"iormesh/internal/schema"
)

// TestFileLifecycle: mkdir, O_EXCL create, rename while open, ftruncate,
// utimensat on an fd, a cross-directory move, unlink and rmdir pass the
// file-lifecycle golden. Its ftruncate is declared on the renamed path, so
// the golden itself proves the fd followed its file (M2 step 5).
func TestFileLifecycle(t *testing.T) {
	got, want := fileScenario(t, "file-lifecycle")
	t.Run("wrong expectations are caught", func(t *testing.T) {
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			"fd kept its pre-rename path": func(x *normalize.Expectation) {
				setPath(x, schema.KindFtruncate, "$tmp/file-lifecycle.d/a.dat")
			},
			"move to another directory": func(x *normalize.Expectation) {
				setNewPath(x, 1, "$tmp/file-lifecycle.d/c.dat")
			},
		})
	})
}

// TestFileLinks: symlink (its text unresolved), readlink, a no-follow
// statx, link, and opens through both links pass the file-links golden.
func TestFileLinks(t *testing.T) {
	got, want := fileScenario(t, "file-links")
	t.Run("wrong expectations are caught", func(t *testing.T) {
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			"symlink text resolved": func(x *normalize.Expectation) {
				setPath(x, schema.KindSymlink, "$tmp/file-links.d/target.dat")
			},
			"statx of an fd": func(x *normalize.Expectation) {
				setFlags(x, schema.KindStatx, 0x1000) // AT_EMPTY_PATH
			},
		})
	})
}

// TestFileAttrs: chmod and chown by path, the dirfd-relative forms of a
// SecureDirectoryStream (dup, fchmodat, fchown) and the four f*xattr calls
// pass the file-attrs golden; fchmodat's relative name is declared
// resolved against its directory fd.
func TestFileAttrs(t *testing.T) {
	got, want := fileScenario(t, "file-attrs")
	t.Run("wrong expectations are caught", func(t *testing.T) {
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			"fchmodat path left relative": func(x *normalize.Expectation) {
				setPath(x, schema.KindFchmodat, "file-attrs.dat")
			},
			"dup2 instead of dup": func(x *normalize.Expectation) {
				x.Events = slices.Clone(x.Events)
				for i, e := range x.Events {
					if e.Event == schema.KindDup {
						x.Events[i].Event = schema.KindDup2
					}
				}
			},
		})
	})
}

// fileScenario traces one single-process file scenario and asserts that
// only it was traced and that it passes its golden.
func fileScenario(t *testing.T, scenario string) (normalize.Trace, normalize.Expectation) {
	t.Helper()
	enableParallelIfRequested(t)
	h := newHarness(t)
	run := traceJudged(t, h, startScenario(scenario))
	assertOnlyTracedPIDs(t, run.Trace.Events, run.pids)
	got := normalized(t, h, run, scenario)
	want := expectation(t, scenario)
	assertGolden(t, got, want)
	return got, want
}

// setPath sets args.path on the first declared event of kind.
func setPath(x *normalize.Expectation, kind, path string) {
	x.Events = slices.Clone(x.Events)
	for i, e := range x.Events {
		if e.Event == kind {
			x.Events[i].Args.Path = &path
			return
		}
	}
}

// setNewPath sets args.new_path on the n-th (0-based) declared event that
// has one.
func setNewPath(x *normalize.Expectation, n int, path string) {
	x.Events = slices.Clone(x.Events)
	for i, e := range x.Events {
		if e.Args.NewPath == nil {
			continue
		}
		if n == 0 {
			x.Events[i].Args.NewPath = &path
			return
		}
		n--
	}
}

// setFlags sets args.flags on the first declared event of kind.
func setFlags(x *normalize.Expectation, kind string, flags uint32) {
	x.Events = slices.Clone(x.Events)
	for i, e := range x.Events {
		if e.Event == kind {
			x.Events[i].Args.Flags = &flags
			return
		}
	}
}

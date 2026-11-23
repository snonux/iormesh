package ctl

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// pruneFixture is the build fixture at t0+10m with foreign entries
// planted where prune looks: none of them may ever be deleted.
type pruneFixture struct {
	*buildFixture
	// foreign are the planted entries prune must leave alone.
	foreign []string
	// liveTable is a table file of app2's session, old by its own name
	// but still needed by the session's newer segments; orphanTable one
	// of a session with no segment left.
	liveTable, orphanTable string
}

func newPruneFixture(t *testing.T) *pruneFixture {
	t.Helper()
	f := &pruneFixture{buildFixture: newBuildFixture(t)}
	f.e.now = func() time.Time { return windowT0.Add(10 * time.Minute) }
	host := filepath.Join(f.store, "billing", "app2")
	old := "app2.example.org-ffffffff-20200101T000000Z-000000"
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(filepath.Join(outside, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(p string) {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, p string) {
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(outside, old+".parquet")
	write(target)
	write(filepath.Join(outside, "x", old+".parquet"))
	f.foreign = []string{
		filepath.Join(host, "notes.txt"),
		filepath.Join(host, "."+old+".parquet.Xy12"),                                     // rsync's in-flight temporary
		filepath.Join(host, old+".parquet"),                                              // a symlink, to a file outside
		filepath.Join(host, "app2.example.org-eeeeeeee-20200101T000000Z-000000.parquet"), // a directory
		filepath.Join(f.store, "billing", old+".parquet"),
		filepath.Join(f.store, "billing", "linked"), // a symlinked host directory
		target,
		filepath.Join(outside, "x", old+".parquet"),
	}
	write(f.foreign[0])
	write(f.foreign[1])
	link(target, f.foreign[2])
	if err := os.Mkdir(f.foreign[3], 0o700); err != nil {
		t.Fatal(err)
	}
	write(f.foreign[4])
	link(filepath.Join(outside, "x"), f.foreign[5])
	f.liveTable = filepath.Join(host, "hostB.example.org-a1b2c3d4-"+windowT0.Format("20060102T150405Z")+
		"-000000.methods.parquet")
	f.orphanTable = filepath.Join(host, "app2.example.org-deadbeef-20200101T000000Z-000000.methods.parquet")
	write(f.liveTable)
	write(f.orphanTable)
	return f
}

// relNames are paths below the store, as prune prints them.
func (f *pruneFixture) relNames(t *testing.T, paths ...string) []string {
	t.Helper()
	var out []string
	for _, p := range paths {
		rel, err := filepath.Rel(f.store, p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, rel)
	}
	slices.Sort(out)
	return out
}

// listed is the paths the output's lines name after prefix.
func listed(out, prefix string) []string {
	var got []string
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			got = append(got, strings.TrimSpace(rest))
		}
	}
	slices.Sort(got)
	return got
}

// TestPrune (task yh4): prune -older-than lists the store's own segment
// files whose range ended before the cutoff — and a table file only
// once no segment of its session stays — and deletes nothing; -apply
// deletes exactly those and says so. Foreign entries (other names, dot
// files, symlinks, anything outside a project's host directory) are
// never touched, and the store still builds.
func TestPrune(t *testing.T) {
	f := newPruneFixture(t)
	// Cutoff t0+5m: seq 0 and 1 of each host end by then (start + 1m
	// interval + 1m slack), seq 2 at t0+8m does not.
	want := f.relNames(t, f.app1Files[0], f.app1Files[1], f.app2Files[0], f.app2Files[1], f.orphanTable)
	out := f.mustRun(t, "prune", "-older-than", "5m")
	if got := listed(out, "would delete  "); !slices.Equal(got, want) {
		t.Errorf("listed %v, want %v\n%s", got, want, out)
	}
	if !strings.Contains(out, "-apply") {
		t.Errorf("a listing does not say how to delete:\n%s", out)
	}
	for _, p := range append([]string{f.app1Files[0], f.orphanTable}, f.foreign...) {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("a listing deleted %s: %v", p, err)
		}
	}
	out = f.mustRun(t, "prune", "-older-than", "5m", "-apply")
	if got := listed(out, "deleted       "); !slices.Equal(got, want) {
		t.Errorf("deleted %v, want %v\n%s", got, want, out)
	}
	for _, p := range []string{f.app1Files[0], f.app1Files[1], f.app2Files[0], f.app2Files[1], f.orphanTable} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s not deleted: %v", p, err)
		}
	}
	for _, p := range append([]string{f.app1Files[2], f.app2Files[2], f.liveTable}, f.foreign...) {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("-apply deleted %s: %v", p, err)
		}
	}
	if out := f.mustRun(t, "prune", "-older-than", "5m"); len(listed(out, "would delete  ")) != 0 {
		t.Errorf("a second listing found more:\n%s", out)
	}
	// The planted table file holds no table: it goes before the build
	// reads the session's tables.
	if err := os.Remove(f.liveTable); err != nil {
		t.Fatal(err)
	}
	f.mustRun(t, "build")
}

// TestPruneProject: -project limits prune to that project's host
// directories; given twice, each file is listed and deleted once.
func TestPruneProject(t *testing.T) {
	f := newPruneFixture(t)
	want := f.relNames(t, f.app1Files[0], f.app1Files[1])
	if got := listed(f.mustRun(t, "prune", "-older-than", "5m", "-project", "shop", "-project", "shop"),
		"would delete  "); !slices.Equal(got, want) {
		t.Errorf("-project shop twice listed %v, want %v", got, want)
	}
	out := f.mustRun(t, "prune", "-older-than", "5m", "-project", "shop", "-apply")
	if got := listed(out, "deleted       "); !slices.Equal(got, want) {
		t.Errorf("deleted %v, want %v\n%s", got, want, out)
	}
	if _, err := os.Lstat(f.app2Files[0]); err != nil {
		t.Errorf("prune -project shop deleted billing's %s", f.app2Files[0])
	}
}

// TestPruneSymlinkedProject: a project directory that is a symlink is
// not the store's: -project naming it is refused before anything is
// listed, and without -project it is skipped, its target untouched.
func TestPruneSymlinkedProject(t *testing.T) {
	f := newPruneFixture(t)
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Rename(filepath.Join(f.store, "shop"), elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(f.store, "shop")); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(elsewhere, "app1", filepath.Base(f.app1Files[0]))
	if code := f.run("prune", "-older-than", "5m", "-project", "shop", "-apply"); code != exitError ||
		!strings.Contains(f.stderr.String(), "nothing collected for project shop") {
		t.Errorf("prune -project of a symlinked project: exit %d, %s", code, f.stderr)
	}
	out := f.mustRun(t, "prune", "-older-than", "5m", "-apply")
	if strings.Contains(out, "shop/") {
		t.Errorf("prune followed the symlinked project:\n%s", out)
	}
	if _, err := os.Lstat(moved); err != nil {
		t.Errorf("the symlinked project's segment is gone: %v", err)
	}
}

// TestPruneRefusals: no -older-than, a non-positive one or arguments
// are usage errors; a project the store does not hold is an error.
func TestPruneRefusals(t *testing.T) {
	f := newPruneFixture(t)
	for _, args := range [][]string{{}, {"-older-than", "0s"}, {"-older-than", "-1h"},
		{"-older-than", "1h", "extra"}, {"-older-than", "1h", "-project", "../x"}} {
		if code := f.run(append([]string{"prune"}, args...)...); code != exitUsage {
			t.Errorf("prune %v: exit %d, want %d (%s)", args, code, exitUsage, f.stderr)
		}
	}
	if code := f.run("prune", "-older-than", "1h", "-project", "nosuch"); code != exitError ||
		!strings.Contains(f.stderr.String(), "nosuch") {
		t.Errorf("prune of an unknown project: exit %d, %s", code, f.stderr)
	}
	for _, p := range []string{f.app1Files[0], f.orphanTable} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("a refused prune deleted %s", p)
		}
	}
}

// TestPruneForgetsInServiceDB (task gk4): prune -apply makes the service
// database forget the deleted segments, so a full build afterwards no
// longer shows a service seen only in them — while one seen in a
// segment that stays, and the operator's naming rules, remain.
func TestPruneForgetsInServiceDB(t *testing.T) {
	f := newBuildFixture(t)
	const early, late = "198.51.100.1:5432", "198.51.100.2:6379"
	lateDialer(t, f, early, late) // early only in seq 0 (t0), late in seq 1 (t0+3m)
	f.mustRun(t, "services", "name", "198.51.100.*", "dbs")
	f.mustRun(t, "build")
	if all := endpointsOf(t, f.mustRun(t, "show", "services", "-json")); !slices.Contains(all, early) {
		t.Fatalf("full build's services %v: want %s", all, early)
	}
	// Cutoff t0+3m: every seq 0 (its range ends at t0+2m) goes, seq 1
	// (ends at t0+5m) stays.
	f.e.now = func() time.Time { return windowT0.Add(10 * time.Minute) }
	out := f.mustRun(t, "prune", "-older-than", "7m")
	if !strings.Contains(out, "service database") {
		t.Errorf("the listing does not say the database forgets them too:\n%s", out)
	}
	out = f.mustRun(t, "prune", "-older-than", "7m", "-apply")
	if !strings.Contains(out, "service database: forgot") {
		t.Errorf("prune -apply does not report the database update:\n%s", out)
	}
	f.mustRun(t, "build")
	got := endpointsOf(t, f.mustRun(t, "show", "services", "-json"))
	if slices.Contains(got, early) || !slices.Contains(got, late) {
		t.Errorf("services after prune -apply and a full build %v: want %s and not %s", got, late, early)
	}
	if edges := f.mustRun(t, "show", "edges", "-json"); strings.Contains(edges, "198.51.100.1") {
		t.Errorf("edges still name the peer seen only in pruned segments:\n%s", edges)
	}
	if list := f.mustRun(t, "services", "-db", f.db); strings.Contains(list, "198.51.100.1") {
		t.Errorf("services list still names the pruned peer:\n%s", list)
	}
	if rules := f.mustRun(t, "services", "rules"); !strings.Contains(rules, "dbs") {
		t.Errorf("prune dropped the operator's rule:\n%s", rules)
	}
}

// TestPruneNoServiceDB: prune -apply before any build (no service
// database yet) deletes the files and creates no database.
func TestPruneNoServiceDB(t *testing.T) {
	f := newBuildFixture(t)
	f.e.now = func() time.Time { return windowT0.Add(10 * time.Minute) }
	out := f.mustRun(t, "prune", "-older-than", "7m", "-apply")
	if len(listed(out, "deleted       ")) == 0 {
		t.Errorf("nothing deleted:\n%s", out)
	}
	if _, err := os.Lstat(f.db); !os.IsNotExist(err) {
		t.Errorf("prune created the service database: %v", err)
	}
}

// TestPruneDBFailureDeletesNothing (negative): when the service
// database cannot be updated, prune -apply deletes no file — the
// database never keeps counting a file that is gone.
func TestPruneDBFailureDeletesNothing(t *testing.T) {
	f := newBuildFixture(t)
	f.mustRun(t, "build")
	if err := os.Chmod(f.db, 0o644); err != nil { // refused: readable by others
		t.Fatal(err)
	}
	f.e.now = func() time.Time { return windowT0.Add(10 * time.Minute) }
	if code := f.run("prune", "-older-than", "7m", "-apply"); code != exitError ||
		!strings.Contains(f.stderr.String(), "nothing deleted") {
		t.Errorf("prune over an unusable database: exit %d, %s", code, f.stderr)
	}
	if _, err := os.Lstat(f.app1Files[0]); err != nil {
		t.Errorf("a failed database update still deleted %s: %v", f.app1Files[0], err)
	}
}

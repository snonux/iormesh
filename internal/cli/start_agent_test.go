package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeInstalledAgent makes a jar standing in for the installed one in a
// private temporary directory (trustedAgent accepts the caller's own
// files) and a symlink to it in another, and returns both.
func fakeInstalledAgent(t *testing.T) (jar, link string) {
	t.Helper()
	lib := filepath.Join(t.TempDir(), "lib")
	mustMkdir(t, lib)
	jar = filepath.Join(lib, "iormesh-agent.jar")
	mustWrite(t, jar, []byte("jar"), 0o644)
	if err := os.Chmod(jar, 0o644); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(t.TempDir(), "a.jar")
	if err := os.Symlink(jar, link); err != nil {
		t.Fatal(err)
	}
	return jar, link
}

// TestStartForwardsResolvedAgent (task kf4's review, B1): a start given a
// symlink to the installed jar passes the check, and the unit's argv
// carries the jar's resolved path, never the symlink the caller could
// re-point between the check and the unit's start; -spool goes out as
// the checked, cleaned pinned path; and the unit's run is told to
// re-check (--setenv).
func TestStartForwardsResolvedAgent(t *testing.T) {
	c, _, traces := rootedControl(t, newFakeSystemd(), nil)
	jar, link := fakeInstalledAgent(t)
	c.confine.agent = jar
	spool := c.confine.spool("shop")
	cfg, err := c.parseStart([]string{"-pattern", "x", "-project", "shop", "-trace-dir", traces, "-spool", spool + "/",
		"-agent", link, "-wait", "0"})
	if err != nil {
		t.Fatal(err)
	}
	argv := cfg.unitArgs(c.self)
	for _, want := range []string{"-agent=" + jar, "-spool=" + spool, "--setenv=IORMESH_CONFINED_UNIT=1"} {
		if !slices.Contains(argv, want) {
			t.Errorf("unit argv lacks %q: %q", want, argv)
		}
	}
	for _, a := range argv {
		if strings.Contains(a, link) {
			t.Errorf("unit argv carries the caller's symlink: %q", a)
		}
	}
}

// TestStartForwardsCleanSpool: without -agent too, the unit gets the
// spool path the confinement compared (cleaned), not the caller's
// spelling of it.
func TestStartForwardsCleanSpool(t *testing.T) {
	c, _, traces := rootedControl(t, newFakeSystemd(), nil)
	spool := c.confine.spool("shop")
	cfg, err := c.parseStart([]string{"-pattern", "x", "-project", "shop", "-trace-dir", traces + "/",
		"-spool", spool + "//", "-wait", "0"})
	if err != nil {
		t.Fatal(err)
	}
	argv := cfg.unitArgs(c.self)
	for _, want := range []string{"-spool=" + spool, "-trace-dir=" + traces} {
		if !slices.Contains(argv, want) {
			t.Errorf("unit argv lacks %q: %q", want, argv)
		}
	}
}

// TestStartRefusesOtherAgent: a jar that is not the installed one is
// refused by start even when it is trusted otherwise.
func TestStartRefusesOtherAgent(t *testing.T) {
	c, _, traces := rootedControl(t, newFakeSystemd(), nil)
	_, link := fakeInstalledAgent(t)
	other, _ := fakeInstalledAgent(t)
	c.confine.agent = other
	_, err := c.parseStart([]string{"-pattern", "x", "-project", "shop", "-trace-dir", traces, "-spool", c.confine.spool("shop"),
		"-agent", link, "-wait", "0"})
	if err == nil || !strings.Contains(err.Error(), "-agent") {
		t.Fatalf("parseStart = %v, want -agent refused", err)
	}
}

// TestConfinementSymlinkedAgent: the check compares the run's resolved
// -agent with the installed path or its real path (an /opt that is a
// symlink); an unresolved symlink to the same jar is refused.
func TestConfinementSymlinkedAgent(t *testing.T) {
	jar, link := fakeInstalledAgent(t)
	k := confinement{traceRoot: defaultTraceDir, spoolRoot: defaultSpoolRoot, agent: link}
	cfg := runConfig{project: "p", traceDir: defaultTraceDir, spool: "/run/iormesh/spool/p", agent: jar}
	if err := k.check(cfg); err != nil {
		t.Errorf("real path of a symlinked install refused: %v", err)
	}
	k.agent = jar
	cfg.agent = link
	if err := k.check(cfg); err == nil {
		t.Error("an unresolved symlink to the installed jar passed")
	}
}

// TestUnitRunIsConfined: the unit's run of a start is confined by the
// environment start sets, whoever's binary runs it.
func TestUnitRunIsConfined(t *testing.T) {
	env := func(k string) string { return map[string]string{unitConfinedEnv: "1"}[k] }
	k, ok := runConfinement("/home/u/iormesh", env)
	if !ok || k.agent != installedAgent || k.traceRoot != defaultTraceDir {
		t.Errorf("unit run confinement = %+v, %v", k, ok)
	}
	if _, ok := runConfinement("/home/u/iormesh", func(string) string { return "" }); ok {
		t.Error("a plain run is confined")
	}
}

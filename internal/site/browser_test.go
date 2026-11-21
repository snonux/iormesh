package site

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"iormesh/internal/schema"
	"iormesh/internal/viewmodel"
)

// browserEnv opts in to TestBrowserSelftest: it starts a real browser,
// which `mage test` must not need (docs/testing.md "Local site").
const browserEnv = "IORMESH_SITE_BROWSER"

// TestBrowserSelftest loads a site of the hostile sample model from
// file:// in headless Chrome or Chromium at #/selftest — the page renders
// every screen, service, edge, thread, process, conversation and step in
// turn — and checks the DOM reports every route rendered and the
// console stayed empty: an exception, a CSP refusal (which is how a
// request or an injected script would show) or any other message fails
// it. Set IORMESH_SITE_BROWSER=1 (or a browser's path) to run it.
func TestBrowserSelftest(t *testing.T) {
	bin := browser(t)
	dir, err := Write(filepath.Join(t.TempDir(), "sites"), schema.Graph{}, sampleModel(hostile))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--headless=new", "--disable-gpu", "--no-first-run",
		"--user-data-dir="+t.TempDir(), "--enable-logging=stderr", "--v=0", "--virtual-time-budget=5000",
		"--dump-dom", "file://"+filepath.Join(dir, IndexName)+"#/selftest")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v\n%s", bin, err, stderr.String())
	}
	if want := "selftest: ok " + strconv.Itoa(selftestRoutes(sampleModel(hostile))) + " routes"; !strings.Contains(string(out), want) {
		t.Errorf("selftest did not pass with %q; DOM:\n%s", want, out)
	}
	for _, line := range strings.Split(stderr.String(), "\n") {
		if strings.Contains(line, "CONSOLE") || strings.Contains(line, "Refused to") {
			t.Errorf("console: %s", line)
		}
	}
}

// selftestRoutes is how many routes app.js's selftest renders for m:
// the five screens, three guarded deep links, then every service, edge,
// routed edge, route flag, thread, process, conversation and step — so
// a screen the selftest skipped (the routed edges, task bf4) fails the
// count.
func selftestRoutes(m viewmodel.Model) int {
	// The 3: the out-of-range and hostile deep links the selftest checks
	// land on their "no such …" guard (mesh/route/999, mesh/flag/-1,
	// mesh/route/__proto__).
	n := 5 + 3 + len(m.Services) + len(m.Edges) + len(m.Routes) + len(m.Health.RouteFlags) + len(m.Threads) +
		len(m.Processes)
	for _, c := range m.Conversations {
		n += 1 + len(c.Steps)
	}
	return n
}

// browser is the browser to run, or skips the test.
func browser(t *testing.T) string {
	t.Helper()
	v := os.Getenv(browserEnv)
	if v == "" {
		t.Skipf("set %s=1 to load the site in a headless browser", browserEnv)
	}
	if v != "1" {
		return v
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	t.Skip("no Chrome or Chromium on PATH")
	return ""
}

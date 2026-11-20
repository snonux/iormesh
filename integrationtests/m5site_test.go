package integrationtests

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"iormesh/internal/site"
	"iormesh/internal/viewmodel"
)

// siteBrowserEnv opts in to loading the site in a headless browser, as
// internal/site's TestBrowserSelftest does (docs/testing.md "Local
// site"): 1 finds Chrome or Chromium on PATH, anything else is the
// browser's path.
const siteBrowserEnv = "IORMESH_SITE_BROWSER"

// modelBlock is the page's inert model block (internal/site page.go).
var modelBlock = regexp.MustCompile(`(?s)<script type="application/json" id="model">(.*?)</script>`)

// assertSiteWalk: the build's newest site embeds the very model show
// and the TUI read, under a CSP that loads nothing; walked in that model,
// orders is a named service whose conversation reaches the rename and its
// stack. With IORMESH_SITE_BROWSER set the page is loaded from file:// in
// a headless browser: its selftest renders every route with no console
// message, and the rename step's route shows its stack; set without a
// browser on PATH, the test fails rather than pass unchecked.
func assertSiteWalk(t *testing.T, b *viewsBuild, c viewmodel.Conversation) {
	index, err := site.Latest(filepath.Join(b.dir, "sites")) // what `iormeshctl open` opens
	if err != nil {
		t.Fatal(err)
	}
	page, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	m := modelBlock.FindSubmatch(page)
	var embedded viewmodel.Model
	if m == nil || json.Unmarshal(m[1], &embedded) != nil {
		t.Fatalf("%s holds no model block", index)
	}
	if !reflect.DeepEqual(embedded, b.model) {
		t.Error("the site's model is not model.json's: two readings of one build")
	}
	if !strings.Contains(string(page), `content="default-src 'none'; script-src 'sha256-`) ||
		regexp.MustCompile(`(?i)\b(src|href)\s*=\s*["']?[a-z]*:?//`).Match(page) {
		t.Errorf("%s: no default-src 'none' CSP, or a remote reference", index)
	}
	walkEmbedded(t, embedded, c)
	if os.Getenv(siteBrowserEnv) == "" {
		t.Logf("set %s=1 to load the site in a headless browser", siteBrowserEnv)
		return
	}
	assertSiteInBrowser(t, index, c)
}

// walkEmbedded walks the embedded model as the page does: the orders
// service, a conversation that touches it, the rename step's stack.
func walkEmbedded(t *testing.T, m viewmodel.Model, want viewmodel.Conversation) {
	t.Helper()
	named := false
	for _, s := range m.Services {
		named = named || s.Name == "orders" && s.Named
	}
	for _, c := range m.Conversations {
		if !named || !strings.Contains(strings.Join(c.Services, " "), "orders") || c.ID != want.ID {
			continue
		}
		s := c.Steps[stepIndex(t, c, "rename", "order-42.json.tmp")]
		w := want.Steps[stepIndex(t, want, "rename", "order-42.json.tmp")]
		if len(s.Stack) == 0 || !reflect.DeepEqual(s.Stack, w.Stack) {
			t.Errorf("the site's rename step stack %q, want %q", s.Stack, w.Stack)
		}
		return
	}
	t.Errorf("the site's model does not lead from a named orders to conversation %s", want.ID)
}

// assertSiteInBrowser loads the page twice: #/selftest (every route,
// and the console must stay empty — an exception, a CSP refusal or a
// request would print there) and the rename step's route.
func assertSiteInBrowser(t *testing.T, index string, c viewmodel.Conversation) {
	t.Helper()
	bin := siteBrowser()
	if bin == "" {
		// Opted in: a check that silently did not run would read as passed.
		t.Fatalf("%s is set, but no Chrome or Chromium is on PATH", siteBrowserEnv)
	}
	dom, console := dumpDOM(t, bin, "file://"+index+"#/selftest")
	if !regexp.MustCompile(`selftest: ok [1-9]\d* routes`).MatchString(dom) {
		t.Errorf("selftest did not pass; DOM:\n%s", dom)
	}
	for _, l := range console {
		t.Errorf("console: %s", l)
	}
	n := stepIndex(t, c, "rename", "order-42.json.tmp") + 1
	dom, _ = dumpDOM(t, bin, "file://"+index+"#/conv/"+c.ID+"/step/"+strconv.Itoa(n))
	for _, want := range []string{"DataStore#save(DataStore.java:", "iormeshctl query -ref"} {
		if !strings.Contains(dom, want) {
			t.Errorf("step %d's route lacks %q", n, want)
		}
	}
}

// siteBrowser is the browser IORMESH_SITE_BROWSER names, "" if none.
func siteBrowser() string {
	v := os.Getenv(siteBrowserEnv)
	if v != "1" {
		return v
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// dumpDOM loads url headless and returns the DOM and the console lines
// (and CSP refusals) the browser logged. Root needs --no-sandbox: Chrome
// refuses to sandbox as root, and the page is our own, built by this
// test.
func dumpDOM(t *testing.T, bin, url string) (string, []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	args := []string{"--headless=new", "--disable-gpu", "--no-first-run",
		// No network from the browser process itself: only the page is under test.
		"--disable-background-networking", "--disable-component-update", "--disable-sync",
		"--no-default-browser-check", "--user-data-dir=" + t.TempDir(),
		"--enable-logging=stderr", "--v=0", "--virtual-time-budget=5000", "--dump-dom", url}
	if os.Geteuid() == 0 {
		args = append([]string{"--no-sandbox"}, args...)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v\n%s", bin, err, stderr.String())
	}
	var console []string
	for _, l := range strings.Split(stderr.String(), "\n") {
		if strings.Contains(l, "CONSOLE") || strings.Contains(l, "Refused to") {
			console = append(console, l)
		}
	}
	return string(out), console
}

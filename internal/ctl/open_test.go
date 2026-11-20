package ctl

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"iormesh/internal/site"
	"iormesh/internal/viewmodel"
)

// sitePath is the site line of build's or open's output.
func sitePath(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(line, "site       "); ok {
			return p
		}
	}
	t.Fatalf("no site line in:\n%s", out)
	return ""
}

// TestOpenSite: every build writes a site; open prints the newest and
// hands it to the opener, or says there is none; a rebuild of the same
// store rewrites the same folder byte for byte, and a build of changed
// data adds a folder and keeps the old one.
func TestOpenSite(t *testing.T) {
	f := newBuildFixture(t)
	if code := f.run("open"); code != exitError || !strings.Contains(f.stderr.String(), "no site") {
		t.Fatalf("open before a build: exit %d, %s", code, f.stderr)
	}
	first := sitePath(t, f.mustRun(t, "build"))
	page, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	out := f.mustRun(t, "open")
	if sitePath(t, out) != first || !strings.Contains(out, "no desktop opener") {
		t.Errorf("open without an opener:\n%s", out)
	}
	var opened string
	f.e.opener = func(p string) error { opened = p; return nil }
	if f.mustRun(t, "open"); opened != first {
		t.Errorf("opened %q, want %q", opened, first)
	}
	if again := sitePath(t, f.mustRun(t, "build")); again != first {
		t.Fatalf("rebuild wrote %s, want %s", again, first)
	}
	if b, _ := os.ReadFile(first); !bytes.Equal(b, page) {
		t.Fatal("rebuild changed index.html")
	}
	if err := os.Remove(f.app1Files[len(f.app1Files)-1]); err != nil {
		t.Fatal(err)
	}
	second := sitePath(t, f.mustRun(t, "build"))
	if second == first {
		t.Fatal("changed data, same site folder")
	}
	if _, err := os.Stat(first); err != nil {
		t.Errorf("the older run's site is gone: %v", err)
	}
	if f.mustRun(t, "open"); opened != second {
		t.Errorf("open after the second build opened %q, want %q", opened, second)
	}
}

// TestSiteRendersTheModel: the site's page embeds exactly the model
// show reads (model.json) — one view model, three surfaces — and the
// site folder carries the SVG exports it links.
func TestSiteRendersTheModel(t *testing.T) {
	f := newMicroFixture(t, true)
	index, err := site.Latest(filepath.Join(f.builds, "shop", sitesDirName))
	if err != nil {
		t.Fatal(err)
	}
	page, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	const open = `<script type="application/json" id="model">`
	i := bytes.Index(page, []byte(open))
	if i < 0 {
		t.Fatal("no model in the page")
	}
	data := page[i+len(open):]
	data = data[:bytes.Index(data, []byte("</script>"))]
	var embedded, shown viewmodel.Model
	if err := json.Unmarshal(data, &embedded); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(f.shopOutput(t, modelFileName)), &shown); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(embedded, shown) || len(shown.Conversations) == 0 {
		t.Error("the site's model differs from model.json")
	}
	for _, name := range []string{"graph.svg", "sequence/0001.svg"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(index), name)); err != nil {
			t.Error(err)
		}
	}
}

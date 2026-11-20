package ctl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"iormesh/internal/report"
)

// readReport reads the report.json in dir back.
func readReport(t *testing.T, dir string) report.Report {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, reportFileName))
	if err != nil {
		t.Fatal(err)
	}
	var r report.Report
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// fileMethods is the causing methods the rollup names for path.
func fileMethods(r report.Report, path string) []string {
	for _, p := range r.Processes {
		for _, f := range p.Files {
			if f.Path == path {
				return f.Methods
			}
		}
	}
	return nil
}

// TestBuildReportMethods: the rollup names the Java method that opened
// a file through the session's checked methods table; with the table
// damaged it is left out (named, exit 1) and the file names no method —
// a missing table is no guess.
func TestBuildReportMethods(t *testing.T) {
	f := newBuildFixture(t)
	h, methods := javaTrace()
	files := writeJavaSegment(t, f.store, "appJ", h, methods)
	f.mustRun(t, "build", "-project", "shop")
	dir := filepath.Join(f.builds, "shop")
	if got := fileMethods(readReport(t, dir), "/data/order-42.json"); len(got) != 1 ||
		got[0] != "com.shop.DataStore#save" {
		t.Errorf("order-42.json opened by %v, want com.shop.DataStore#save", got)
	}
	out := f.mustRun(t, "show", "report", "-project", "shop")
	if !strings.Contains(out, "/data/order-42.json: opened for reading 1x, 0 B read; by com.shop.DataStore#save") {
		t.Errorf("show report:\n%s", out)
	}
	for _, p := range files {
		if strings.HasSuffix(p, ".methods.parquet") {
			if err := os.WriteFile(p, []byte("PAR1 damaged PAR1"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if code := f.run("build", "-project", "shop"); code != exitError ||
		!strings.Contains(f.stdout.String(), "bad        shop/appJ/") {
		t.Fatalf("damaged table: exit %d\n%s%s", code, f.stdout, f.stderr)
	}
	if got := fileMethods(readReport(t, dir), "/data/order-42.json"); len(got) != 0 {
		t.Errorf("a damaged table still named %v", got)
	}
}

// TestShowReportWithoutReport: show report on a build without
// report.json says so instead of printing half a view.
func TestShowReportWithoutReport(t *testing.T) {
	f := newBuildFixture(t)
	f.mustRun(t, "build")
	if err := os.Remove(filepath.Join(f.builds, "billing+shop", reportFileName)); err != nil {
		t.Fatal(err)
	}
	if code := f.run("show", "report"); code != exitError ||
		!strings.Contains(f.stderr.String(), "no report for the build of billing+shop") {
		t.Errorf("exit %d: %s", code, f.stderr)
	}
}

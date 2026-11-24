package eval

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestParseReply: the first action line wins; ANSWER: takes the rest.
func TestParseReply(t *testing.T) {
	for reply, want := range map[string]Action{
		"QUERY -file '*/a.json'":                  {ActQuery, "-file '*/a.json'"},
		"I will look.\n```\nSOURCE a/B.java\n```": {ActSource, "a/B.java"},
		"ANSWER: DataStore#save\nvia tmp":         {ActAnswer, "DataStore#save\nvia tmp"},
		"  ANSWER:x\nQUERY -conn c":               {ActAnswer, "x\nQUERY -conn c"},
		"QUERY -conn c\nANSWER: y":                {ActQuery, "-conn c"},
	} {
		if got, ok := ParseReply(reply); !ok || got != want {
			t.Errorf("%q: %+v %v, want %+v", reply, got, ok, want)
		}
	}
	for _, reply := range []string{"", "no action here", "QUERY", "answer: lower case"} {
		if got, ok := ParseReply(reply); ok {
			t.Errorf("%q parsed as %+v", reply, got)
		}
	}
}

// TestSplitArgs: quotes group, whitespace separates, an open quote is
// an error.
func TestSplitArgs(t *testing.T) {
	for in, want := range map[string][]string{
		`-file '*/order 42.json' -follow`: {"-file", "*/order 42.json", "-follow"},
		`-method "a#b"   -thread x`:       {"-method", "a#b", "-thread", "x"},
		`-app ''`:                         {"-app", ""},
		``:                                nil,
	} {
		if got, err := SplitArgs(in); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	if _, err := SplitArgs(`-file 'open`); err == nil {
		t.Error("an unterminated quote was accepted")
	}
}

// TestCheckQueryArgs: only the slice flags pass; the harness's own
// flags and positional arguments are refused, and Ctl.Query hands the
// refusal to the model without running anything.
func TestCheckQueryArgs(t *testing.T) {
	for _, ok := range [][]string{{"-file", "*/a"}, {"--conn=c1", "-follow"}, {"-method", "a#b", "-to", "12:00:00"}, nil} {
		if err := CheckQueryArgs(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range [][]string{{"-store", "/"}, {"-db=/x"}, {"-max-bytes", "9"}, {"-project", "p"},
		{"file"}, {"-file"}, {"-conn", "c", "extra"}} {
		if err := CheckQueryArgs(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	out, err := Ctl{Path: "/nonexistent/iormeshctl"}.Query(context.Background(), []string{"-store", "/"})
	if err != nil || !strings.HasPrefix(out, "error: flag -store is not allowed") {
		t.Errorf("refused query: %q %v", out, err)
	}
}

// TestSourceTree: files below the root are read (cut at the cap);
// "..", absolute paths and symlinks out of the root are not.
func TestSourceTree(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	for path, data := range map[string]string{filepath.Join(root, "a", "A.java"): "class A {}",
		filepath.Join(root, "big.txt"): strings.Repeat("x", 100), outside: "secret"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	tree := SourceTree{Root: root, MaxBytes: 10}
	if got, err := tree.Source("a/A.java"); err != nil || got != "class A {}" {
		t.Errorf("a/A.java: %q %v", got, err)
	}
	if got, _ := tree.Source("big.txt"); !strings.HasPrefix(got, "xxxxxxxxxx\n[cut at 10 bytes]") {
		t.Errorf("big.txt: %q", got)
	}
	for _, p := range []string{"../" + filepath.Base(filepath.Dir(outside)) + "/secret", outside, "link", "nope"} {
		if got, err := tree.Source(p); err != nil || !strings.HasPrefix(got, "error: ") || strings.Contains(got, root) {
			t.Errorf("%s: %q %v, want an error naming no local path", p, got, err)
		}
	}
	if got, err := tree.List(); err != nil || !reflect.DeepEqual(got, []string{"a/A.java", "big.txt"}) {
		t.Errorf("list %q %v", got, err)
	}
}

// TestCtlArgs: build, show report and query get the harness's store,
// scratch database and builds directory and the projects, query the
// slice cap after them and the model's flags last; a query that exits
// non-zero is the model's to read.
func TestCtlArgs(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "iormeshctl")
	script := "#!/bin/sh\necho \"$@\"\ncase \"$*\" in *fail*) exit 2;; esac\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	c := Ctl{Path: bin, Store: "/s", DB: "/w/db", Builds: "/w/b", Projects: []string{"p"}, SliceBytes: 4096}
	ctx := context.Background()
	if err := c.Build(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Report(ctx); err != nil || got != "show report -builds /w/b -store /s -project p\n" {
		t.Errorf("report: %q %v", got, err)
	}
	got, err := c.Query(ctx, []string{"-file", "*/a"})
	if err != nil || got != "query -db /w/db -store /s -project p -max-bytes 4096 -file */a\n" {
		t.Errorf("query: %q %v", got, err)
	}
	if got, err := c.Query(ctx, []string{"-thread", "fail"}); err != nil ||
		!strings.HasSuffix(got, "-thread fail\nerror: iormeshctl query exited 2") {
		t.Errorf("failing query: %q %v", got, err)
	}
}

// TestCtlQueryKeepsStderr (task al4): query's stderr stays in the tool
// output, after the slice — the resolved window line ("query: window
// FROM .. TO") tells the model where a time of day landed, and a refusal
// tells it what to fix.
func TestCtlQueryKeepsStderr(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "iormeshctl")
	script := "#!/bin/sh\necho '{\"event\":\"openat\"}'\necho 'query: window A .. B' >&2\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	c := Ctl{Path: bin, Store: "/s", SliceBytes: 4096}
	got, err := c.Query(context.Background(), []string{"-from", "2025-11-20T00:00:00"})
	if err != nil || got != "{\"event\":\"openat\"}\nquery: window A .. B\n" {
		t.Errorf("query: %q %v", got, err)
	}
}

// TestCtlSandbox: every iormeshctl run gets the scratch HOME and XDG
// directories, and one that hangs is cut by the timeout.
func TestCtlSandbox(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "iormeshctl")
	script := "#!/bin/sh\necho \"$HOME $XDG_CONFIG_HOME $XDG_DATA_HOME\"\ncase \"$*\" in *hang*) sleep 5;; esac\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	c := Ctl{Path: bin, Store: "/s", Home: "/w/home", SliceBytes: 4096, Timeout: 100 * time.Millisecond}
	ctx := context.Background()
	if got, err := c.Report(ctx); err != nil || got != "/w/home /w/home/config /w/home/data\n" {
		t.Errorf("env: %q %v", got, err)
	}
	start := time.Now()
	got, err := c.Query(ctx, []string{"-thread", "hang"})
	if err != nil || !strings.Contains(got, "error: iormeshctl query: timed out") || time.Since(start) > 3*time.Second {
		t.Errorf("a hanging query: %q %v after %v", got, err, time.Since(start))
	}
}

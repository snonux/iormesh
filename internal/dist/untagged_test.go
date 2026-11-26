package dist

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testCommit = "6fca2931c0ffee6fca2931c0ffee6fca2931c0ff"

// TestCheckUntagged pins decision #40's gate: an untagged build is cut
// from a clean HEAD that is not the release tag, so the build names
// exactly one commit and never stands in for a release `mage release`
// could cut.
func TestCheckUntagged(t *testing.T) {
	tests := []struct {
		describe string
		ok       bool
	}{
		{"6fca293", true},              // no v tag in the history
		{"v0.3.0-2-gf912990", true},    // past the tag
		{"v0.2.9-14-g6fca293\n", true}, // past an older tag
		{"v0.3.0", false},              // the release commit: mage release
		{"v0.3.0\n", false},
		{"6fca293-dirty", false},
		{"v0.3.0-2-gf912990-dirty", false},
		{"", false},
	}
	for _, tt := range tests {
		err := CheckUntagged(tt.describe, testVersion)
		if (err == nil) != tt.ok {
			t.Errorf("CheckUntagged(%q) = %v, want ok %v", tt.describe, err, tt.ok)
		}
	}
}

// TestUntaggedNote: the note names the commit and says it is no
// release, in its first line; a commit that is no full hash is refused.
func TestUntaggedNote(t *testing.T) {
	note, err := UntaggedNote(testCommit+"\n", "v0.2.9-14-g6fca293\n", testVersion)
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(string(note), "\n")
	for _, want := range []string{"0.3.0", "untagged", testCommit, "NOT a release"} {
		if !strings.Contains(first, want) {
			t.Errorf("first line %q lacks %q", first, want)
		}
	}
	if !strings.Contains(string(note), "v0.2.9-14-g6fca293") {
		t.Errorf("note lacks the describe:\n%s", note)
	}
	for _, bad := range []string{"", "6fca293", strings.ToUpper(testCommit), testCommit + "x"} {
		if _, err := UntaggedNote(bad, "6fca293", testVersion); err == nil {
			t.Errorf("UntaggedNote(%q) succeeded", bad)
		}
	}
}

// TestPackUntaggedMarker: packed as an Extra, the note sits in every
// archive beside the artifacts, so an unpacked untagged build still
// says what it is.
func TestPackUntaggedMarker(t *testing.T) {
	distDir := fakeDist(t, testVersion)
	note, err := UntaggedNote(testCommit, "6fca293", testVersion)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), UntaggedFile)
	if err := os.WriteFile(marker, note, 0o600); err != nil {
		t.Fatal(err)
	}
	_, paths := packTo(t, distDir, marker)
	for _, archive := range paths[:2] {
		found := false
		for _, e := range readArchive(t, archive) {
			if strings.HasSuffix(e.hdr.Name, "/"+UntaggedFile) {
				found = string(e.data) == string(note)
			}
		}
		if !found {
			t.Errorf("%s: no %s entry holding the note", archive, UntaggedFile)
		}
	}
}

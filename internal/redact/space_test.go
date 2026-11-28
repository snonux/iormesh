package redact

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"unicode"
)

// spaceFile lists the BMP code points unicode.IsSpace reports, the white
// space authCredential splits at (through strings.Fields). The Java
// agent's CorpusTest compares Redaction.space with it for every char.
const spaceFile = "testdata/space.txt"

// TestSpaceTable keeps spaceFile equal to unicode.IsSpace over the BMP
// (task xp4), so the list the Java side is checked against is Go's own.
func TestSpaceTable(t *testing.T) {
	b, err := os.ReadFile(spaceFile)
	if err != nil {
		t.Fatal(err)
	}
	var file []string
	for line := range strings.Lines(string(b)) {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			file = append(file, line)
		}
	}
	var want []string
	for r := rune(0); r <= 0xFFFF; r++ {
		if unicode.IsSpace(r) {
			want = append(want, fmt.Sprintf("%04X", r))
		}
	}
	if !slices.Equal(file, want) {
		t.Errorf("%s = %q, unicode.IsSpace says %q", spaceFile, file, want)
	}
}

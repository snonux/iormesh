package buildgate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"iormesh/internal/containerbuild"
)

// builderClang is the clang each tracer builder installs, as its
// Containerfile's CLANG_VERSION pins it: the version whose BPF object was
// last loaded on el9's verifier. The bytecode depends on the compiler -
// Debian clang 19.1.7 once tested a slot index on a copy that el9 refused
// (task il4, decision #31) - so a new clang needs that check again before
// its version goes here (docs/testing.md, "Builder clang pin", task ll4).
var builderClang = map[string]string{
	"tracer-amd64": "22.1.8",
	"tracer-arm64": "19.1.7",
}

var (
	clangArgRE   = regexp.MustCompile(`(?m)^ARG CLANG_VERSION=(\S+)\s*$`)
	clangCheckRE = regexp.MustCompile(`(?m)^RUN clang --version .*grep -F "clang version \$\{CLANG_VERSION\} "\s*$`)
)

// containerfileClang returns the clang version a builder Containerfile pins:
// its one ARG CLANG_VERSION, which a RUN line must check against the
// installed clang - an ARG nothing checks would pin nothing.
func containerfileClang(text string) (string, error) {
	args := clangArgRE.FindAllStringSubmatch(text, -1)
	if len(args) != 1 {
		return "", fmt.Errorf("want exactly one ARG CLANG_VERSION=<version>, found %d", len(args))
	}
	if !clangCheckRE.MatchString(text) {
		return "", fmt.Errorf("no RUN line checks clang --version against ${CLANG_VERSION}")
	}
	return args[0][1], nil
}

// TestBuilderClangPinned: every tracer builder pins its clang, and the pin
// is the recorded one. Changing a Containerfile's CLANG_VERSION (or the
// distro moving its clang, which fails the image build) must go through the
// el9 verifier check first; this test is where that is enforced.
func TestBuilderClangPinned(t *testing.T) {
	seen := map[string]bool{}
	for _, target := range containerbuild.Targets() {
		if !target.Tracer || target.Containerfile == "" {
			continue
		}
		seen[target.Name] = true
		raw, err := os.ReadFile(filepath.Join(repoRoot(t), target.Containerfile))
		if err != nil {
			t.Fatalf("read %s: %v", target.Containerfile, err)
		}
		got, err := containerfileClang(string(raw))
		if err != nil {
			t.Errorf("%s: %v", target.Containerfile, err)
			continue
		}
		if want := builderClang[target.Name]; got != want {
			t.Errorf("%s installs clang %s, recorded %q: re-run the el9 verifier "+
				"check with this clang (docs/testing.md, \"Builder clang pin\") "+
				"before updating builderClang", target.Containerfile, got, want)
		}
	}
	for name := range builderClang {
		if !seen[name] {
			t.Errorf("builderClang names %s, which is no tracer target", name)
		}
	}
}

// TestContainerfileClangRejects: the parser refuses a Containerfile that
// does not really pin its clang.
func TestContainerfileClangRejects(t *testing.T) {
	check := `RUN clang --version | head -n 1 | grep -F "clang version ${CLANG_VERSION} "`
	cases := map[string]struct {
		text string
		want string // "" for a pin, else a substring of the error
	}{
		"pinned":      {"ARG CLANG_VERSION=19.1.7\n" + check + "\n", ""},
		"no arg":      {check + "\n", "found 0"},
		"two args":    {"ARG CLANG_VERSION=19.1.7\nARG CLANG_VERSION=20.1.0\n" + check + "\n", "found 2"},
		"unchecked":   {"ARG CLANG_VERSION=19.1.7\nRUN dnf install -y clang\n", "no RUN line"},
		"check unset": {"ARG CLANG_VERSION=19.1.7\nRUN clang --version\n", "no RUN line"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := containerfileClang(tc.text)
			switch {
			case tc.want == "" && (err != nil || got != "19.1.7"):
				t.Errorf("containerfileClang() = %q, %v; want 19.1.7", got, err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("containerfileClang() = %q, %v; want an error with %q", got, err, tc.want)
			}
		})
	}
}

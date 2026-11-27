package redact

import (
	"os"
	"regexp"
	"slices"
	"testing"
)

// agentRedaction is the Java agent's copy of the header policy, relative
// to this package's directory (where go test runs).
const agentRedaction = "../../agent/core/src/iormesh/agent/http/Redaction.java"

// javaList returns the string literals of the Java list or set constant
// named name ("HEADER_DENY = List.of(...)") in src.
func javaList(t *testing.T, src, name string) []string {
	t.Helper()
	decl := regexp.MustCompile(name + `\s*=\s*(?:java\.util\.)?(?:List|Set)\.of\(([^)]*)\)`)
	m := decl.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("%s: no %s = List.of(...)/Set.of(...)", agentRedaction, name)
	}
	var out []string
	for _, lit := range regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(m[1], -1) {
		out = append(out, lit[1])
	}
	return out
}

// normalized returns globs folded as denied folds them.
func normalized(globs []string) []string {
	out := make([]string, len(globs))
	for i, g := range globs {
		out[i] = normalizeKey(g)
	}
	return out
}

// TestAgentPolicyMatchesDefault keeps the Java agent's header lists (task
// 0j4) and auth schemes (task np4) in step with Default: the agent masks
// before the spool, so a header it lets through would reach disk even
// though the tracer masks it again afterwards. The lists are compared
// entry for entry, normalized. The JWT shape test has no list to compare;
// the same value tables in TestHeaderMasksCredentialValues and the
// agent's HttpProbeTest keep the two implementations in step.
func TestAgentPolicyMatchesDefault(t *testing.T) {
	b, err := os.ReadFile(agentRedaction)
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	d := Default()
	for _, c := range []struct {
		java string
		goes []string
	}{
		{"HEADER_DENY", d.HeaderDeny},
		{"AUTH_SCHEMES", d.AuthSchemes},
		{"URL_HEADERS", d.URLHeaders},
	} {
		got, want := normalized(javaList(t, src, c.java)), normalized(c.goes)
		if !slices.Equal(got, want) {
			t.Errorf("Redaction.%s = %q, want Default()'s %q", c.java, got, want)
		}
	}
}

package target

import (
	"errors"
	"testing"
)

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"*RestartApp*", "/usr/bin/java -cp /w.jar iormesh.workload.scenarios.RestartApp /tmp/x", true},
		{"*RestartApp*", "/usr/bin/java -jar /w.jar restart", false},
		{"*iormesh.workload.scenarios.RestartApp*", "java -cp a iormesh.workload.scenarios.RestartApp d", true},
		// '*' crosses '/' and ' ', unlike path.Match.
		{"/usr/*/java *", "/usr/lib/jvm/bin/java -version", true},
		{"java", "java", true},
		{"java", "java -version", false},
		{"java*", "javac", true},
		{"?ava", "java", true},
		{"?ava", "ava", false},
		{"a*b*c", "aXXbYYc", true},
		{"a*b*c", "aXXbYY", false},
		{"*a", "", false},
		// Escapes make '*' and '?' literal.
		{`x\*y`, "x*y", true},
		{`x\*y`, "xay", false},
		{`what\?`, "what?", true},
		{"**x**", "x", true},
	}
	for _, c := range cases {
		g, err := CompileGlob(c.pattern)
		if err != nil {
			t.Fatalf("compile %q: %v", c.pattern, err)
		}
		if got := g.Match(c.s); got != c.want {
			t.Errorf("%q.Match(%q) = %v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}

func TestCompileGlobRejects(t *testing.T) {
	cases := []struct {
		pattern string
		want    error
	}{
		{"", ErrEmptyPattern},
		{"*", ErrTooBroad},
		{"??*", ErrTooBroad},
		{`abc\`, nil}, // trailing escape: an error, but no sentinel
	}
	for _, c := range cases {
		_, err := CompileGlob(c.pattern)
		if err == nil {
			t.Errorf("CompileGlob(%q) succeeded", c.pattern)
			continue
		}
		if c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("CompileGlob(%q) = %v, want %v", c.pattern, err, c.want)
		}
	}
}

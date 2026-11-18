package target

import (
	"errors"
	"fmt"
	"strings"
)

// Glob is a compiled shell-style pattern over a whole command line (or an
// executable path): '*' matches any run of characters, '/' and ' '
// included, '?' exactly one byte, and '\' makes the next character literal.
// That is path.Match's syntax minus character classes and with '*'
// crossing '/': a command line is not a path, and the pattern a user
// writes for a Java app — "*com.example.Main*" — must reach past the
// java binary's path and the class path.
type Glob struct {
	pattern string
}

// Glob compile errors.
var (
	ErrEmptyPattern = errors.New("target: empty pattern")
	// ErrTooBroad: a pattern without one literal character ("*", "??*")
	// matches every process on the host, which a session that traces all
	// matches (decision #23) must never do by accident.
	ErrTooBroad = errors.New("target: pattern has no literal character and would match every process")
)

// CompileGlob checks pattern and returns its Glob.
func CompileGlob(pattern string) (Glob, error) {
	if pattern == "" {
		return Glob{}, ErrEmptyPattern
	}
	literal := false
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*', '?':
		case '\\':
			if i == len(pattern)-1 {
				return Glob{}, fmt.Errorf("target: pattern %q ends in an escape", pattern)
			}
			i++
			literal = true
		default:
			literal = true
		}
	}
	if !literal {
		return Glob{}, fmt.Errorf("%w: %q", ErrTooBroad, pattern)
	}
	return Glob{pattern: pattern}, nil
}

// String returns the pattern as written.
func (g Glob) String() string { return g.pattern }

// Match reports whether the whole of s matches. It is the classic
// backtracking wildcard match: on a mismatch after a '*' it retries with
// that star swallowing one more character, which keeps it linear in
// practice and quadratic at worst — command lines are short.
func (g Glob) Match(s string) bool {
	p := g.pattern
	pi, si := 0, 0
	star, mark := -1, 0
	for si < len(s) {
		if pi < len(p) {
			switch c := p[pi]; {
			case c == '*':
				star, mark = pi, si
				pi++
				continue
			case c == '?':
				pi, si = pi+1, si+1
				continue
			case c == '\\' && pi+1 < len(p) && p[pi+1] == s[si]:
				pi, si = pi+2, si+1
				continue
			case c != '\\' && c == s[si]:
				pi, si = pi+1, si+1
				continue
			}
		}
		if star < 0 {
			return false
		}
		mark++
		pi, si = star+1, mark
	}
	return strings.Trim(p[pi:], "*") == ""
}

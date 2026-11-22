// Package pathtmpl folds an HTTP request path into its template, so the
// controller's per-path views count routes, not ids (PLAN.md decision
// #22's templating step, task xh4): /orders/123 and /orders/124 are both
// /orders/{id}. The trace events keep the raw path (decision #22: path
// segments are verbatim); only the aggregations fold — the service
// database's request keys and the views built from them — so `iormeshctl
// query` still shows what was requested.
//
// Two layers, the first that matches wins:
//
//   - operator rules: path templates from the route map
//     (routes.yaml's path_templates, internal/routemap), such as
//     /shops/{shop}/orders/{order}. A {name} segment matches any one
//     segment, every other segment only itself; a matching path is
//     shown as the rule's text. A rule without placeholders pins a
//     path verbatim, built-in folding included.
//   - built-in folding: a segment that is all digits, a canonical UUID,
//     or a run of at least 16 hex digits with at least one digit becomes
//     {id}. Deliberately conservative: a segment that could be a word
//     or a version (v2, cafe, report-2024) is never folded, since
//     merging two routes is worse than leaving one id unfolded.
//
// Only the path is templated: the query and fragment (whose values the
// agent masked already, decision #22) follow verbatim. Folding is
// idempotent — {id} is no id — so a stored template can be folded again.
//
// The package is cgo-free: iormeshctl runs on a laptop.
package pathtmpl

import (
	"errors"
	"fmt"
	"strings"
)

// ID is what a built-in fold puts in place of an id-like segment.
const ID = "{id}"

// ErrRule marks an operator rule that cannot be used, for errors.Is.
var ErrRule = errors.New("pathtmpl: invalid path template")

// Bounds on the operator's rules: each path is matched against every
// rule in order, so the list stays short, and a rule is a path, not a
// document.
const (
	MaxRules   = 256
	MaxRuleLen = 512
	maxName    = 32 // a placeholder's name
)

// anySeg marks a placeholder segment of a compiled rule; no literal
// segment can hold a brace, so it never equals one.
const anySeg = "{}"

// minHex is the shortest hex run folded: shorter runs (cafe, deadbeef,
// a short commit) are as likely words or names as ids.
const minHex = 16

// Rules are the operator's compiled path templates, in their order. A
// nil *Rules is no rules: Template folds by the built-in rules alone.
type Rules struct {
	rules []rule
}

// rule is one template: its text and its segments, anySeg where a
// placeholder matches any one segment.
type rule struct {
	text string
	segs []string
}

// Compile checks and compiles the operator's templates; no templates is
// a nil *Rules. A template is an absolute path of printable ASCII
// without space, query or fragment, whose segments are literal or a
// whole {name} placeholder (name: letters, digits, underscore); a
// duplicate is refused rather than silently shadowed.
func Compile(templates []string) (*Rules, error) {
	if len(templates) == 0 {
		return nil, nil
	}
	if len(templates) > MaxRules {
		return nil, fmt.Errorf("%w: at most %d path templates", ErrRule, MaxRules)
	}
	r := &Rules{}
	seen := map[string]bool{}
	for _, t := range templates {
		segs, err := parse(t)
		if err != nil {
			return nil, err
		}
		if seen[t] {
			return nil, fmt.Errorf("%w: %q listed twice", ErrRule, t)
		}
		seen[t] = true
		r.rules = append(r.rules, rule{text: t, segs: segs})
	}
	return r, nil
}

// parse checks one template and splits it into segments.
func parse(t string) ([]string, error) {
	if len(t) > MaxRuleLen {
		return nil, fmt.Errorf("%w: longer than %d bytes", ErrRule, MaxRuleLen)
	}
	if !strings.HasPrefix(t, "/") {
		return nil, fmt.Errorf("%w: %q does not start with /", ErrRule, t)
	}
	for i := 0; i < len(t); i++ {
		if c := t[i]; c <= ' ' || c > '~' || c == '?' || c == '#' {
			return nil, fmt.Errorf("%w: %q: only printable ASCII, no space, ? or #", ErrRule, t)
		}
	}
	segs := strings.Split(t[1:], "/")
	for i, s := range segs {
		if !strings.ContainsAny(s, "{}") {
			continue
		}
		name, open := strings.CutPrefix(s, "{")
		name, closed := strings.CutSuffix(name, "}")
		if !open || !closed || !validName(name) {
			return nil, fmt.Errorf("%w: %q: a placeholder is a whole segment {name}", ErrRule, t)
		}
		segs[i] = anySeg
	}
	return segs, nil
}

// validName reports whether a placeholder's name is 1 to maxName
// letters, digits or underscores.
func validName(n string) bool {
	if n == "" || len(n) > maxName {
		return false
	}
	for i := 0; i < len(n); i++ {
		c := n[i]
		if !(c == '_' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z') {
			return false
		}
	}
	return true
}

// Template is p's template: the first operator rule's text that matches
// p's path, else p with its id-like segments folded to {id}. The query
// and fragment are kept as they are.
func (r *Rules) Template(p string) string {
	path, rest := split(p)
	if r != nil {
		segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
		for _, ru := range r.rules {
			if ru.matches(path, segs) {
				return ru.text + rest
			}
		}
	}
	return fold(path) + rest
}

// Builtin is p's template by the built-in rules alone.
func Builtin(p string) string {
	return (*Rules)(nil).Template(p)
}

// split cuts p before its query or fragment.
func split(p string) (path, rest string) {
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		return p[:i], p[i:]
	}
	return p, ""
}

// matches reports whether a path (split into segs) fits the rule: as
// many segments, each literal equal and each placeholder non-empty.
func (ru rule) matches(path string, segs []string) bool {
	if !strings.HasPrefix(path, "/") || len(segs) != len(ru.segs) {
		return false
	}
	for i, s := range ru.segs {
		if (s == anySeg && segs[i] == "") || (s != anySeg && s != segs[i]) {
			return false
		}
	}
	return true
}

// fold replaces every id-like segment of path with ID; a path without
// one is returned as is, unallocated.
func fold(path string) string {
	segs := strings.Split(path, "/")
	changed := false
	for i, s := range segs {
		if IsID(s) {
			segs[i], changed = ID, true
		}
	}
	if !changed {
		return path
	}
	return strings.Join(segs, "/")
}

// IsID reports whether a path segment is id-like by the built-in rules:
// all ASCII digits, a canonical UUID (8-4-4-4-12 hex digits, either
// case), or at least minHex hex digits including a digit.
func IsID(s string) bool {
	switch {
	case s == "":
		return false
	case allOf(s, isDigit):
		return true
	case isUUID(s):
		return true
	default:
		return len(s) >= minHex && allOf(s, isHex) && strings.ContainsAny(s, "0123456789")
	}
}

// isUUID reports whether s is a canonical UUID.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch i {
		case 8, 13, 18, 23:
			if s[i] != '-' {
				return false
			}
		default:
			if !isHex(s[i]) {
				return false
			}
		}
	}
	return true
}

func allOf(s string, ok func(byte) bool) bool {
	for i := 0; i < len(s); i++ {
		if !ok(s[i]) {
			return false
		}
	}
	return true
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

func isHex(c byte) bool {
	return isDigit(c) || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

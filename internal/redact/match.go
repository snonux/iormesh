package redact

import (
	"strings"
	"unicode/utf8"
)

// normalizeKey folds a key for matching: lower case (Unicode-aware) and
// without the separators '-', '_' and '.'. One deny glob "*apikey*" then
// covers "--api-key", "API_KEY", "-Dapi.key" and "apiKey" alike, which a
// case- and separator-sensitive list would need four entries for.
func normalizeKey(key string) string {
	key = strings.ToLower(key)
	return strings.Map(func(r rune) rune {
		switch r {
		case '-', '_', '.':
			return -1
		}
		return r
	}, key)
}

// denied reports whether key matches one of the deny globs. Both sides are
// normalized, so a policy may write its globs in any case or separator
// style. An empty key is matched like any other: only a glob that matches
// everything ("*") denies it, so "?=secret" is masked under the default
// query policy and "=x" in argv is not.
func denied(globs []string, key string) bool {
	k := normalizeKey(key)
	for _, g := range globs {
		if globMatch(normalizeKey(g), k) {
			return true
		}
	}
	return false
}

// globMatch matches s against pattern rune by rune: '*' matches any run of
// runes (empty included), '?' exactly one rune, everything else itself.
// No escapes and no classes — deny globs name keys, and keys are folded by
// normalizeKey before they get here. It backtracks only to the last '*',
// which keeps it linear in practice and free of recursion.
func globMatch(pattern, s string) bool {
	p, i := 0, 0
	star, mark := -1, 0
	for i < len(s) {
		pr, pw := utf8.DecodeRuneInString(pattern[p:])
		sr, sw := utf8.DecodeRuneInString(s[i:])
		switch {
		case p < len(pattern) && pr == '*':
			star, mark = p, i
			p += pw
		case p < len(pattern) && (pr == '?' || pr == sr):
			p += pw
			i += sw
		case star >= 0:
			// Let the last '*' swallow one more rune and retry after it.
			_, mw := utf8.DecodeRuneInString(s[mark:])
			mark += mw
			p, i = star+1, mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

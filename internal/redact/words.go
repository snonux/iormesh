package redact

import (
	"slices"
	"strings"
	"unicode"
)

// token is one piece of a multi-word element: a word, or the white space
// between two words, kept so the element is rebuilt with its own
// separators.
type token struct {
	text string
	sep  bool
}

// hasSpace reports whether s holds white space, i.e. several words: a
// shell command string (sh -c "mysql -psecret") or a name and its value
// in one element ("--token abc", "Authorization: Bearer x").
func hasSpace(s string) bool {
	return strings.IndexFunc(s, unicode.IsSpace) >= 0
}

// words redacts a multi-word element as a sub-argv with the same rules —
// a denied option masks the next word, a program word scopes FlagRules —
// and rebuilds it with its separators. Quoted runs stay one word first
// ("-H 'Authorization: Bearer x'"); when that leaves the element one
// word, it is split at every space instead, so the recursion always makes
// progress. A denied option in the last word masks the next element.
func (p Policy) words(st *argvState, s string) (string, bool) {
	toks := splitWords(s, true)
	if len(toks) == 1 {
		toks = splitWords(s, false)
	}
	sub := argvState{progs: slices.Clone(st.progs)}
	var b strings.Builder
	for _, t := range toks {
		if t.sep {
			b.WriteString(t.text)
			continue
		}
		b.WriteString(p.word(&sub, t.text))
	}
	return b.String(), sub.maskNext
}

// splitWords cuts s into words and separators. With quotes, a run between
// matching ' or " quotes (an unclosed one runs to the end) is part of its
// word even where it holds spaces, as a shell would read it.
func splitWords(s string, quotes bool) []token {
	var toks []token
	start, inSep := 0, false
	var quote rune
	for i, r := range s {
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		sp := unicode.IsSpace(r)
		switch {
		case i == 0:
			inSep = sp
		case sp != inSep:
			toks = append(toks, token{text: s[start:i], sep: inSep})
			start, inSep = i, sp
		}
		if !sp && quotes && (r == '\'' || r == '"') {
			quote = r
		}
	}
	return append(toks, token{text: s[start:], sep: inSep})
}

// colonPair redacts a "Name: value" element — an HTTP header as curl -H or
// --header=… pass it, or a colon pair like "--api_key:x" or "pass:x"
// (openssl) — when the name is a denied header or option: the value
// becomes Mask, the separator stays. A denied name with nothing after the
// colon takes its value from the next word or element. ok is false when
// arg is no such pair, so "jdbc:…", "-XX:+Flag" or "host:8080" go on to
// the other forms.
func (p Policy) colonPair(arg string) (out string, next, ok bool) {
	i := strings.IndexByte(arg, ':')
	if i <= 0 {
		return "", false, false
	}
	name, rest := arg[:i], arg[i+1:]
	bare := strings.TrimLeft(name, `'"`)
	if !isName(bare) || !(denied(p.HeaderDeny, bare) || denied(p.ArgvDeny, optionKey(bare))) {
		return "", false, false
	}
	value := strings.TrimLeftFunc(rest, unicode.IsSpace)
	sep := rest[:len(rest)-len(value)]
	if value == "" {
		return name + ":" + sep, true, true
	}
	return name + ":" + sep + Mask, false, true
}

// isName reports a header or option name: letters, digits, '-', '_' and
// '.', at least one of them.
func isName(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("-_.", r)
	}) < 0
}

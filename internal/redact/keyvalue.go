package redact

import "strings"

// keyValues is the generic last pass over an argv element (task rm4): it
// masks the value of every "denied key, separator, value" it finds in the
// text, whatever syntax carries the pair — JSON ({"password":"x"},
// {"k": "x"}), single-quoted dicts ({'password': 'x'}), escaped JSON
// inside a shell string ({\"token\":\"x\"}), YAML (password: x), a query
// or env pair in a longer string (/p?token=x&a=1, export PASSWORD=x; …).
// The specific forms of argvElem run first; this pass catches what none
// of them knows, so a new container syntax needs no new parser.
//
// A pair is a key (name bytes, see isKeyByte) that ArgvDeny denies, then
// an optional closing quote (escaped or not), optional blanks, ':' or
// '=', optional blanks, and a value: a quoted string up to its matching
// unescaped quote (or to the end when unclosed), or a bare run up to a
// delimiter (see bareEnd). Only the value becomes Mask; key, quotes and
// separators stay. A value starting with '{' or '[' is a nested object:
// its own pairs are judged one by one. open reports a denied key whose
// separator ends the text ({"password": then "x"} as two elements), so
// the caller masks the next element.
//
// The scan is linear: each separator looks back only over the bytes
// since the previous separator, and a masked value is skipped whole.
func (p Policy) keyValues(s string) (out string, open bool) {
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); i++ {
		if s[i] != ':' && s[i] != '=' || !p.deniedKeyBefore(s, i) {
			continue
		}
		from, to, ok := valueSpan(s, i+1)
		if !ok {
			open = open || from == len(s)
			continue
		}
		b.WriteString(s[last:from])
		b.WriteString(Mask)
		last = to
		i = to - 1
	}
	if last == 0 {
		return s, open
	}
	b.WriteString(s[last:])
	return b.String(), open
}

// deniedKeyBefore reports whether the separator at s[sep] follows a key
// ArgvDeny denies: blanks, then a closing quote and its escaping
// backslashes, are skipped back over first ("password" : …,
// \"password\":…).
func (p Policy) deniedKeyBefore(s string, sep int) bool {
	j := sep
	for j > 0 && isBlank(s[j-1]) {
		j--
	}
	if j > 0 && isQuote(s[j-1]) {
		j--
		for j > 0 && s[j-1] == '\\' {
			j--
		}
	}
	end := j
	for j > 0 && isKeyByte(s[j-1]) {
		j--
	}
	return j < end && denied(p.ArgvDeny, optionKey(s[j:end]))
}

// valueSpan returns the [from, to) range of the value that starts after
// blanks at s[at], and false when there is none to mask: a nested '{' or
// '[', an empty bare run, or the end of s (then from is len(s)).
func valueSpan(s string, at int) (from, to int, ok bool) {
	for at < len(s) && isBlank(s[at]) {
		at++
	}
	if at == len(s) {
		return at, at, false
	}
	esc := at
	for esc < len(s) && s[esc] == '\\' {
		esc++
	}
	if esc < len(s) && isQuote(s[esc]) {
		return esc + 1, quotedEnd(s, esc+1, s[esc], esc-at), true
	}
	if s[at] == '{' || s[at] == '[' {
		return at, at, false
	}
	end := bareEnd(s, at)
	return at, end, end > at
}

// quotedEnd returns where the string opened by quote q (escaped with k
// backslashes, k > 0 inside an already quoted shell string) ends: at the
// first q whose run of n preceding backslashes is that same level, i.e.
// n-k is a multiple of 2(k+1) — n == k closes, an escaped quote one
// level deeper (n = 2k+1) does not. The returned index is the start of
// the closing backslash run, so the escaping stays with the quote. An
// unclosed string runs to the end of s.
func quotedEnd(s string, from int, q byte, k int) int {
	for i := from; i < len(s); i++ {
		if s[i] != q {
			continue
		}
		n := 0
		for i-n-1 >= from && s[i-n-1] == '\\' {
			n++
		}
		if n >= k && (n-k)%(2*(k+1)) == 0 {
			return i - k
		}
	}
	return len(s)
}

// bareEnd returns where an unquoted value starting at s[at] ends: at
// white space, a quote, a backslash or one of ",;&})]".
func bareEnd(s string, at int) int {
	if n := strings.IndexAny(s[at:], " \t\r\n\v\f'\"\\,;&})]"); n >= 0 {
		return at + n
	}
	return len(s)
}

// isKeyByte reports a byte of a key: an ASCII letter or digit, '-', '_',
// '.', or any byte of a non-ASCII rune (keys may be Unicode, and the scan
// goes backward byte by byte; it stops at an ASCII byte, so a key never
// starts inside a rune).
func isKeyByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '-' || c == '_' || c == '.' || c >= 0x80
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' }

func isQuote(c byte) bool { return c == '"' || c == '\'' }

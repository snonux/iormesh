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
// an optional closing quote (escaped or not) and closing brackets or
// parens (a subscript or call, os.environ["PASSWORD"] = x; task op4),
// optional blanks, ':' or '=', the rest of an operator run ("=>", ":=",
// "==", see operatorEnd), optional blanks, and a value: a quoted string
// up to its matching unescaped quote (or to the end when unclosed), an
// object or array up to its matching bracket (see bracketEnd), or a bare
// run up to a delimiter (see bareEnd). Only the value becomes Mask; key, quotes and separators
// stay. An object or array under a denied key is masked whole — its inner
// keys ({"secrets":{"db":"x"}}) need not be denied themselves; under any
// other key the scan goes on inside it, pair by pair. open reports a
// denied key whose
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
		from, to, ok := valueSpan(s, operatorEnd(s, i+1))
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
// ArgvDeny denies; keyEnd finds where that key ends.
func (p Policy) deniedKeyBefore(s string, sep int) bool {
	end := keyEnd(s, sep)
	j := end
	for j > 0 && isKeyByte(s[j-1]) {
		j--
	}
	return j < end && denied(p.ArgvDeny, optionKey(s[j:end]))
}

// keyEnd returns where the key before the separator at s[sep] ends:
// blanks, then closing brackets and parens (a subscript or call,
// d["password"] = …, getenv(TOKEN) = …; task op4), then a closing quote
// with its escaping backslashes and the blanks inside it are skipped back
// over ("password" : …, \"password\":…, " password ":…).
func keyEnd(s string, sep int) int {
	j := blanksBefore(s, sep)
	for j > 0 && (s[j-1] == ']' || s[j-1] == ')') {
		j--
	}
	if j > 0 && isQuote(s[j-1]) {
		j--
		for j > 0 && s[j-1] == '\\' {
			j--
		}
		j = blanksBefore(s, j)
	}
	return j
}

// blanksBefore returns the start of the run of blanks that ends at s[j].
func blanksBefore(s string, j int) int {
	for j > 0 && isBlank(s[j-1]) {
		j--
	}
	return j
}

// operatorEnd returns where the value search starts after a separator
// whose next byte is s[at]: past the rest of an operator run of '=', '>'
// and ':' ("=>" of Ruby, PHP and Perl, ":=", "=="), so the value is
// masked and not the operator (task op4). The run is skipped only when
// what follows cannot start a bare value — the end, a blank, a quote, an
// escaped quote or a delimiter (see bareEnd) — or opens an object or
// array. A run glued to a bare value (password:=x, token==x) may be part
// of the secret, so it stays and is masked with it.
func operatorEnd(s string, at int) int {
	end := at
	for end < len(s) && strings.IndexByte("=>:", s[end]) >= 0 {
		end++
	}
	if end == len(s) || s[end] == '{' || s[end] == '[' || bareEnd(s, end) == end {
		return end
	}
	return at
}

// valueSpan returns the [from, to) range of the value that starts after
// blanks at s[at], and false when there is none to mask: an empty bare
// run, or the end of s (then from is len(s)). An object or array value
// spans its brackets, which are masked with it.
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
		return at, bracketEnd(s, at), true
	}
	end := bareEnd(s, at)
	return at, end, end > at
}

// bracketEnd returns the index just past the '}' or ']' that closes the
// bracket at s[open], or len(s) when it is unclosed. Brackets of either
// kind nest; brackets inside a quoted string do not count — a quote and
// its run of escaping backslashes open a string that quotedEnd closes at
// the same escape level. Only the matching kind closes a level: a '}'
// or ']' that does not match the innermost open bracket means malformed
// input whose value end cannot be known, so the value runs to the end of
// s — closing early there leaked what followed (rm4 review round 2), and
// over-masking is the safe side.
func bracketEnd(s string, open int) int {
	var stack []byte // the closer each open level waits for
	for i := open; i < len(s); i++ {
		switch c := s[i]; {
		case c == '{':
			stack = append(stack, '}')
		case c == '[':
			stack = append(stack, ']')
		case c == '}' || c == ']':
			if c != stack[len(stack)-1] {
				return len(s) // malformed: mask to the end
			}
			if stack = stack[:len(stack)-1]; len(stack) == 0 {
				return i + 1
			}
		case c == '\\' || isQuote(c):
			esc := i
			for esc < len(s) && s[esc] == '\\' {
				esc++
			}
			if esc == len(s) || !isQuote(s[esc]) {
				i = esc - 1 // a backslash escaping no quote
				continue
			}
			k := esc - i
			end := quotedEnd(s, esc+1, s[esc], k)
			if end == len(s) {
				return len(s)
			}
			i = end + k // the closing quote
		}
	}
	return len(s)
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
// white space, a quote, one of ",;&})]", or a run of backslashes that
// escapes a quote (the end of an escaped string, …\"}). A backslash
// escaping anything else is part of the value (ab\SEC); so is a blank it
// escapes, since a shell keeps "SEK\ RET" one word (task op4).
func bareEnd(s string, at int) int {
	for i := at; i < len(s); i++ {
		switch c := s[i]; {
		case strings.IndexByte(" \t\r\n\v\f'\",;&})]", c) >= 0:
			return i
		case c == '\\':
			esc := i
			for esc < len(s) && s[esc] == '\\' {
				esc++
			}
			if esc < len(s) && isQuote(s[esc]) {
				return i
			}
			i = esc - 1
			if esc < len(s) && isBlank(s[esc]) {
				// The blank stays in the value; after an even run ("\\ ")
				// it is not escaped, and masking past it over-masks only.
				i = esc
			}
		}
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

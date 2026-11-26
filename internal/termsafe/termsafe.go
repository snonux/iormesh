// Package termsafe makes trace-derived text safe to print to a terminal.
// Hostnames, process names, paths and file names in a build come from
// the servers, any of which may be compromised: an escape sequence in a
// comm or a segment's file name must never reach the operator's
// terminal as a control sequence (it could rewrite the screen, set the
// window title, or worse on some terminals). Every surface that prints
// such text routes it through String (iormeshctl services, the build
// summary and `show`, internal/viewmodel), or JSON for a JSON line
// (iormeshctl query).
package termsafe

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const hexDigits = "0123456789abcdef"

// String returns s unchanged when it is valid UTF-8 and every rune is
// printable, else s Go-quoted ("orders\x1b[2J" prints as
// "\"orders\\x1b[2J\""): visibly odd, never interpreted. Quoting the
// whole string rather than dropping the bad runes keeps two different
// names from printing the same. Validity is checked on its own because
// IndexFunc decodes an invalid byte as U+FFFD, which is printable: a raw
// 0x9B (the 8-bit CSI on terminals that honor C1) would pass otherwise.
func String(s string) string {
	if !utf8.ValidString(s) ||
		strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		return strconv.Quote(s)
	}
	return s
}

// Lines makes multi-line text safe to print as lines (task dm4): s is
// split at its newlines, each line goes through String on its own, and
// every line after the first is prefixed by indent, so a remote tracer's
// error with its unit's journal tail reads as the error's first line
// with the journal lines indented beneath it rather than as one quoted
// string full of \n. A line's leading spaces stay outside its quotes,
// keeping the indentation the text already has. One trailing newline is
// dropped; a carriage return or any other control byte still quotes its
// line. indent itself is printed as it is: callers pass a constant.
func Lines(s, indent string) string {
	var b strings.Builder
	for i, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		if i > 0 {
			b.WriteString("\n" + indent)
		}
		rest := strings.TrimLeft(line, " ")
		b.WriteString(line[:len(line)-len(rest)])
		b.WriteString(String(rest))
	}
	return b.String()
}

// JSON makes one encoded JSON value safe to print: every rune that is
// not printable is written as its \u escape (a surrogate pair above
// U+FFFF), which a JSON reader decodes to the same string. A JSON
// encoder already escapes the ASCII control bytes; what it passes
// through are DEL, the C1 controls (U+0080–U+009F, which some
// terminals honor in UTF-8 form: U+009B is CSI) and the format runes
// (bidi overrides that reorder what the operator reads). The input must
// be valid JSON whose non-ASCII bytes all sit inside strings — what an
// encoder writes — and valid UTF-8 (an encoder replaces invalid bytes);
// an invalid byte is escaped as U+FFFD. Without anything to escape data
// is returned as is, not copied.
func JSON(data []byte) []byte {
	i := firstUnsafe(data)
	if i < 0 {
		return data
	}
	out := make([]byte, 0, len(data)+16)
	out = append(out, data[:i]...)
	for i < len(data) {
		r, size := utf8.DecodeRune(data[i:])
		if r == utf8.RuneError && size == 1 {
			out = appendU(out, unicode.ReplacementChar)
		} else if printableJSON(r) {
			out = append(out, data[i:i+size]...)
		} else {
			out = appendEscaped(out, r)
		}
		i += size
	}
	return out
}

// firstUnsafe is the index of the first byte JSON must look at, or -1:
// plain printable ASCII, the common case, is passed over byte by byte.
func firstUnsafe(data []byte) int {
	for i, b := range data {
		if b >= utf8.RuneSelf || b == 0x7f {
			return i
		}
	}
	return -1
}

// printableJSON reports whether r may stay as it is: printable, or an
// ASCII byte below DEL (the encoder escaped the controls among them,
// and the structure of the value is made of them).
func printableJSON(r rune) bool {
	return r < 0x7f || unicode.IsPrint(r)
}

// appendEscaped appends r as \uXXXX, or as a UTF-16 surrogate pair.
func appendEscaped(dst []byte, r rune) []byte {
	if r > 0xffff {
		r -= 0x10000
		dst = appendU(dst, 0xd800+(r>>10))
		return appendU(dst, 0xdc00+(r&0x3ff))
	}
	return appendU(dst, r)
}

// appendU appends one \uXXXX escape of a 16-bit value.
func appendU(dst []byte, r rune) []byte {
	return append(dst, '\\', 'u', hexDigits[r>>12&0xf], hexDigits[r>>8&0xf],
		hexDigits[r>>4&0xf], hexDigits[r&0xf])
}

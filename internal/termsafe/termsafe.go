// Package termsafe makes trace-derived text safe to print to a terminal.
// Hostnames, process names, paths and file names in a build come from
// the servers, any of which may be compromised: an escape sequence in a
// comm or a segment's file name must never reach the operator's
// terminal as a control sequence (it could rewrite the screen, set the
// window title, or worse on some terminals). Every surface that prints
// such text routes it through String (iormeshctl services, the build
// summary and `show`, internal/viewmodel).
package termsafe

import (
	"strconv"
	"strings"
	"unicode"
)

// String returns s unchanged when every rune is printable, else s Go-
// quoted ("orders\x1b[2J" prints as "\"orders\\x1b[2J\""): visibly odd,
// never interpreted. Quoting the whole string rather than dropping the
// bad runes keeps two different names from printing the same.
func String(s string) string {
	if strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		return strconv.Quote(s)
	}
	return s
}

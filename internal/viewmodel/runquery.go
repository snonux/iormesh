package viewmodel

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// runQuery is the `iormeshctl query` that prints every call of a folded
// run (Step.RunQuery, task vg4): its host, its kernel thread, its file
// and its window — from the first call's instant to just after the
// last's (-to is exclusive). A step's Ref names one event; a run of 420
// needs the window. The file filter follows the fd an open of the path
// returned even before the window, so reads on an fd opened at the
// process's start are printed. What else that thread did to the file
// between the run's calls (a close, a stat) is printed with them. The
// host, thread and path come from the servers: each is a glob escaped
// to match only itself, and the command is quoted into inert shell
// words, so pasting it runs nothing else. Every rune of the result is
// printable (globLiteral turns the others into '?'), so no surface
// passes it through termsafe.String: Go-quoting the finished command
// would break its single quotes and make the path's text shell code
// (task 1i4).
func runQuery(host string, tid uint32, path string, first, last uint64) string {
	words := []string{"iormeshctl", "query", "-host", globLiteral(host),
		"-thread", strconv.FormatUint(uint64(tid), 10), "-file", globLiteral(path),
		"-from", instant(first), "-to", instant(last + 1)}
	for i, w := range words {
		words[i] = ShellWord(w)
	}
	return strings.Join(words, " ")
}

// instant is a wall-clock instant as query's -from and -to take it.
func instant(ns uint64) string {
	return time.Unix(0, int64(ns)).UTC().Format(time.RFC3339Nano)
}

// globLiteral escapes path.Match's metacharacters, so the glob matches
// s alone, except that each rune wild reports becomes the wildcard '?':
// the glob then also matches names differing from s only there, but it
// is printable, so it can be shown and pasted as is (task 1i4).
// path.Match's '?' matches one rune or one invalid byte, as range
// yields them.
func globLiteral(s string) string {
	var b strings.Builder
	for _, r := range s {
		if wild(r) {
			b.WriteByte('?')
			continue
		}
		if strings.ContainsRune(`*?[]\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// wild reports whether r, as range over a string yields it, cannot be
// printed: a control or format (bidi) rune, or utf8.RuneError, which
// stands for an invalid byte (a real U+FFFD is caught with it).
func wild(r rune) bool {
	return !unicode.IsPrint(r) || r == utf8.RuneError
}

// ShellWord is s as one inert POSIX shell word: as is when it holds only
// safe characters, else single-quoted. Every command a surface offers
// for pasting quotes its server-chosen words with it: a run's query
// here, a step's `iormeshctl query -ref` in the TUI (task 3i4).
func ShellWord(s string) string {
	safe := s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			strings.ContainsRune("._/:@+=-", r))
	}) < 0
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

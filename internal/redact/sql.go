package redact

import (
	"regexp"
	"strings"
)

// sqlCredential finds where a SQL statement puts a password (task qp4):
// PostgreSQL's "[ENCRYPTED] PASSWORD 'x'" (CREATE/ALTER ROLE and USER),
// MySQL's "IDENTIFIED [WITH plugin] BY 'x'", "SET PASSWORD [FOR u] = 'x'"
// and the old "PASSWORD('x')", Oracle's "IDENTIFIED BY x". The match ends
// where the value starts. Group 1 is the PASSWORD keyword, which must be
// followed by white space, '=' or '(' and then a quoted value, so prose
// ("password reset") stays; group 2 is IDENTIFIED, whose value after BY
// may be bare (Oracle).
var sqlCredential = regexp.MustCompile(`(?i)\b(?:(password)(?:\s+for\s+\S+)?(?:\s*[=(]\s*|\s+)` +
	`|(identified)(?:\s+with\s+\S+)?\s+by\b)`)

// maskSQL masks the password values of the SQL statements in s, wherever
// they are: a psql -c or mysql -e argument, a shell string holding one.
// Like URL userinfo it does not depend on the policy's lists: these
// keywords always introduce a password.
func maskSQL(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range sqlCredential.FindAllStringSubmatchIndex(s, -1) {
		if !sqlValue(s, m, last) {
			continue
		}
		from, to, _ := valueSpan(s, m[1])
		b.WriteString(s[last:from])
		b.WriteString(Mask)
		last = to
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// sqlValue reports whether the match m of sqlCredential in s is followed
// by a value to mask: not inside a value masked already (before last),
// not a quoted key's name (see inQuotedKey), and a quoted value — or any
// value after IDENTIFIED … BY.
func sqlValue(s string, m []int, last int) bool {
	switch {
	case m[0] < last:
		return false
	case m[2] >= 0:
		return !inQuotedKey(s, m[2], m[3]) && quotedAt(s, m[1])
	}
	_, _, ok := valueSpan(s, m[1])
	return ok
}

// inQuotedKey reports a keyword s[kw:end] that is the whole name of a
// quoted key — a quote (escaped or not), blanks, the keyword, blanks, the
// same quote, blanks, then ':' or '=' (`{" password ":"x"}`) — which
// keyValues masks; its closing quote is not a SQL value's opening one.
func inQuotedKey(s string, kw, end int) bool {
	i := blanksBefore(s, kw)
	j := end
	for j < len(s) && (isBlank(s[j]) || s[j] == '\\') {
		j++
	}
	if i == 0 || j == len(s) || !isQuote(s[i-1]) || s[j] != s[i-1] {
		return false
	}
	j++ // past the closing quote
	for j < len(s) && isBlank(s[j]) {
		j++
	}
	return j < len(s) && (s[j] == ':' || s[j] == '=')
}

// quotedAt reports whether the value at s[at] is a quoted string, its
// quote escaped or not (see valueSpan). sqlCredential's match already
// ends after the blanks before it.
func quotedAt(s string, at int) bool {
	for at < len(s) && s[at] == '\\' {
		at++
	}
	return at < len(s) && isQuote(s[at])
}

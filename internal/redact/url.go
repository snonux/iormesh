package redact

import (
	"net/url"
	"strings"
)

// Text masks every URL embedded in free text — an argument, a header
// value, a "-Ddb.url=jdbc:postgresql://…" property: userinfo (always) and
// the values of denied query parameters. Text without "://" is returned
// unchanged; a bare "?" in an argument is a glob or a question, not a
// query string.
func (p Policy) Text(s string) string {
	spans := urlSpans(s)
	if len(spans) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, sp := range spans {
		b.WriteString(s[last:sp[0]])
		b.WriteString(p.URL(s[sp[0]:sp[1]]))
		last = sp[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// URL masks one URL or request target (what an HTTP server sees:
// "/orders?token=x"): the userinfo of an absolute URL and the values of
// denied query parameters. Parameters are split at '&' and ';' and start at
// the first '?' or ';' after the authority, which also covers JDBC's
// "jdbc:sqlserver://h;user=a;password=b". It never parses strictly — a
// malformed URL is masked as far as it can be read, never passed through.
func (p Policy) URL(raw string) string {
	rest, head := raw, ""
	if i := strings.Index(raw, "://"); i >= 0 {
		authStart := i + len("://")
		authEnd := authStart + strings.IndexAny(raw[authStart:]+"/", "/?#;")
		head = raw[:authStart] + maskUserinfo(raw[authStart:authEnd])
		rest = raw[authEnd:]
	}
	q := strings.IndexAny(rest, "?;")
	if q < 0 {
		return head + rest
	}
	end := len(rest)
	if f := strings.IndexByte(rest[q:], '#'); f >= 0 {
		end = q + f
	}
	return head + rest[:q] + p.maskParams(rest[q:end]) + rest[end:]
}

// maskUserinfo masks the credentials of an authority ("user:pw@host" →
// "user:***@host"). Userinfo without a ':' is masked whole: a lone
// userinfo is how tokens ride in git and API URLs ("https://TOKEN@host").
func maskUserinfo(auth string) string {
	at := strings.LastIndexByte(auth, '@')
	if at < 0 {
		return auth
	}
	user, _, hasPass := strings.Cut(auth[:at], ":")
	if !hasPass {
		return Mask + auth[at:]
	}
	return user + ":" + Mask + auth[at:]
}

// maskParams masks the values of denied parameters in a parameter run that
// starts with its separator ("?a=1&b=2", ";user=a;password=b"). Separators
// and names stay; a parameter without '=' has no value to mask.
func (p Policy) maskParams(params string) string {
	var b strings.Builder
	start := 0
	for i := 0; i <= len(params); i++ {
		if i < len(params) && !strings.ContainsRune("?&;", rune(params[i])) {
			continue
		}
		if i > start {
			b.WriteString(p.maskParam(params[start:i]))
		}
		if i < len(params) {
			b.WriteByte(params[i])
		}
		start = i + 1
	}
	return b.String()
}

// maskParam masks one "name=value" pair. The name is percent-decoded
// before matching, so "pass%77ord" cannot slip past a "*password*" glob.
func (p Policy) maskParam(param string) string {
	name, _, ok := strings.Cut(param, "=")
	if !ok {
		return param
	}
	key := name
	if dec, err := url.QueryUnescape(name); err == nil {
		key = dec
	}
	if denied(p.QueryDeny, key) {
		return name + "=" + Mask
	}
	return param
}

// urlSpans returns the [start, end) byte ranges of the URLs in s. A URL
// starts at its scheme (the run of scheme characters before "://", with
// "jdbc:postgresql" counted whole) and ends at white space or a quote, or
// where the next URL's scheme begins — so "a://x?k=1,b://u:p@y" yields two
// spans and the second userinfo is masked even though it sits inside the
// first URL's query string.
func urlSpans(s string) [][2]int {
	var spans [][2]int
	for off := 0; ; {
		i := strings.Index(s[off:], "://")
		if i < 0 {
			return spans
		}
		i += off
		start := i
		for start > 0 && isSchemeByte(s[start-1]) {
			start--
		}
		if n := len(spans); n > 0 && spans[n-1][1] > start {
			spans[n-1][1] = start
		}
		end := i + len("://")
		for end < len(s) && !strings.ContainsRune(" \t\r\n\"'", rune(s[end])) {
			end++
		}
		spans = append(spans, [2]int{start, end})
		off = i + len("://")
	}
}

// isSchemeByte reports a byte that may appear in a URL scheme (RFC 3986:
// letters, digits, '+', '-', '.'), plus ':' so that "jdbc:mysql" is one
// scheme.
func isSchemeByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
		c >= '0' && c <= '9' || strings.IndexByte("+-.:", c) >= 0
}

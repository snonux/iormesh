package redact

import (
	"net/url"
	"strings"
	"unicode"
)

// Text masks every URL embedded in free text — an argument, a header
// value, a "-Ddb.url=jdbc:postgresql://…" property: userinfo (always) and
// the values of denied query parameters. A URL ends at white space or a
// quote, as in a sentence or a shell string. Besides Oracle's
// "jdbc:oracle:thin:user/password@…" (see maskOracle), text without
// "://" is returned unchanged; a bare "?" in an argument is a glob or a
// question, not a query string.
func (p Policy) Text(s string) string {
	return p.maskURLs(maskOracle(s), false)
}

// wordText is Text for one argv word: the program got it as one
// argument, so a URL in it runs to the word's end, past white space and
// quotes ("https://h/x?token=a'b" masks all of "a'b"). A URL right after
// a quote still ends at the matching quote, which keeps a quoted shell
// word ("'https://h/?a=b'") intact.
func (p Policy) wordText(s string) string {
	return p.maskURLs(maskOracle(s), true)
}

// startsWithURL reports an element that begins with a URL, after at most
// a leading quote.
func startsWithURL(s string) bool {
	spans := urlSpans(s, false)
	return len(spans) > 0 && strings.Trim(s[:spans[0][0]], `'"`) == ""
}

// maskURLs masks every URL urlSpans finds in s; toWordEnd is urlSpans'.
func (p Policy) maskURLs(s string, toWordEnd bool) string {
	spans := urlSpans(s, toWordEnd)
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
// denied parameters. Parameters are split at '&', ';' and '#' and start at
// the first '?', ';' or '#' after the authority, which also covers JDBC's
// "jdbc:sqlserver://h;user=a;password=b" and OAuth's
// "#access_token=x" fragments (a fragment without '=' stays). It never
// parses strictly — a malformed URL is masked as far as it can be read,
// never passed through.
func (p Policy) URL(raw string) string {
	rest, head := raw, ""
	if i := strings.Index(raw, "://"); i >= 0 {
		authStart := i + len("://")
		authEnd := authorityEnd(raw, authStart)
		head = raw[:authStart] + maskUserinfo(raw[authStart:authEnd])
		rest = raw[authEnd:]
	}
	q := strings.IndexAny(rest, "?;#")
	if q < 0 {
		return head + rest
	}
	return head + rest[:q] + p.maskParams(rest[q:])
}

// authorityEnd returns where the authority starting at start ends. An '@'
// before the first '/', '?' or '#' ends the userinfo as usual. Without
// one, an '@' further on still does when the text before that delimiter
// looks like user:password — a ':' followed by something other than a
// port number — because an unescaped '/', '?' or '#' in a password
// ("u:p/ss@h") would otherwise leave its tail in the "path". So
// "https://host/@user" and "https://h:8080/x?mail=a@b" keep their host.
func authorityEnd(raw string, start int) int {
	rest := raw[start:]
	delim := strings.IndexAny(rest, "/?#")
	if delim < 0 {
		delim = len(rest)
	}
	hostStart := 0
	if at := strings.LastIndexByte(rest[:delim], '@'); at >= 0 {
		hostStart = at + 1
	} else if at := strings.IndexByte(rest[delim:], '@'); at >= 0 && looksLikeUserPass(rest[:delim]) {
		hostStart = delim + at + 1
	}
	return start + hostStart + strings.IndexAny(rest[hostStart:]+"/", "/?#;")
}

// looksLikeUserPass reports "user:pass" as opposed to "host:port" or
// "host": a ':' with a non-numeric remainder.
func looksLikeUserPass(s string) bool {
	_, after, ok := strings.Cut(s, ":")
	return ok && strings.TrimLeft(after, "0123456789") != ""
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
// starts with its separator ("?a=1&b=2", ";user=a;password=b",
// "#access_token=x"). Separators and names stay; a parameter without '='
// has no value to mask.
func (p Policy) maskParams(params string) string {
	var b strings.Builder
	start := 0
	for i := 0; i <= len(params); i++ {
		if i < len(params) && !strings.ContainsRune("?&;#", rune(params[i])) {
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
// "jdbc:postgresql" counted whole) and ends where the next URL's scheme
// begins — so "a://x?k=1,b://u:p@y" yields two spans and the second
// userinfo is masked even though it sits inside the first URL's query
// string. Before that it ends at white space or a quote, unless toWordEnd
// is set (s is one argv word, see wordText): then only a quote matching
// one right before the scheme ends it.
func urlSpans(s string, toWordEnd bool) [][2]int {
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
		spans = append(spans, [2]int{start, urlEnd(s, start, i+len("://"), toWordEnd)})
		off = i + len("://")
	}
}

// urlEnd returns where the URL whose scheme starts at start ends, scanning
// from from (just after "://"); see urlSpans for the rules.
func urlEnd(s string, start, from int, toWordEnd bool) int {
	stop := " \t\r\n\"'"
	if toWordEnd {
		stop = ""
		if start > 0 && (s[start-1] == '\'' || s[start-1] == '"') {
			stop = s[start-1 : start]
		}
	}
	if n := strings.IndexAny(s[from:], stop); n >= 0 { // -1 for stop ""
		return from + n
	}
	return len(s)
}

// maskOracle masks the password of Oracle's scheme-less JDBC URLs,
// "jdbc:oracle:<driver>:user/password@<host or descriptor>" (thin, oci):
// without "://" urlSpans does not see them. The credentials run from the
// driver's ':' to the last '@' before white space — a password with '@'
// in it is written quoted (scott/"p@ss"@h), and none of EZConnect
// ("@//h:1521/svc"), "@h:1521:sid" or a descriptor holds one. Text without
// a '/' there ("jdbc:oracle:thin:@//h/svc", "…:scott@h") has no password.
func maskOracle(s string) string {
	// Byte-wise ASCII folding keeps lower's offsets valid in s, which
	// strings.ToLower does not promise (some runes and invalid UTF-8
	// change length).
	lb := []byte(s)
	for i, c := range lb {
		if c >= 'A' && c <= 'Z' {
			lb[i] = c + 'a' - 'A'
		}
	}
	lower := string(lb)
	var b strings.Builder
	last := 0
	for off := 0; ; {
		i := strings.Index(lower[off:], "jdbc:oracle:")
		if i < 0 {
			break
		}
		drv := off + i + len("jdbc:oracle:")
		off = drv
		c := strings.IndexByte(s[drv:], ':')
		if c <= 0 || !isName(s[drv:drv+c]) {
			continue
		}
		creds := drv + c + 1
		word := s[creds:]
		if sp := strings.IndexFunc(word, unicode.IsSpace); sp >= 0 {
			word = word[:sp]
		}
		at := strings.LastIndexByte(word, '@')
		slash := strings.IndexByte(word, '/')
		if at < 0 || slash < 0 || slash > at {
			continue
		}
		b.WriteString(s[last : creds+slash+1])
		b.WriteString(Mask)
		last = creds + at
		off = last
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// isSchemeByte reports a byte that may appear in a URL scheme (RFC 3986:
// letters, digits, '+', '-', '.'), plus ':' so that "jdbc:mysql" is one
// scheme.
func isSchemeByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
		c >= '0' && c <= '9' || strings.IndexByte("+-.:", c) >= 0
}

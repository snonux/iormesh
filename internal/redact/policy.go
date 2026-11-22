package redact

import (
	"path"
	"slices"
	"strings"
)

// Mask replaces every redacted value. It is fixed and short so a reader can
// tell a masked value from a real one, and so its length says nothing about
// the secret's.
const Mask = "***"

// FlagRule denies one option of one program, for options whose name alone
// is too generic to deny everywhere: "-p" is mysql's password but java's
// module path and ssh's port, "-u" is curl's user:password but sort's
// unique. Program is a glob over the basename of any word before the
// option that is not itself an option — argv[0], or a program a wrapper
// runs ("sudo -u root mysql -p…", "env X=1 mysql -p…", "docker login
// -p…" with Program "login"). Flag is the option as written ("-p",
// "--user"). A short Flag ("-p") also masks a value glued to it
// ("-psecret" → "-p***").
type FlagRule struct {
	Program string
	Flag    string
}

// Policy masks secrets before anything is written. Every list is a set of
// deny globs ('*' any run, '?' one rune) matched case-insensitively against
// keys with '-', '_' and '.' removed (see normalizeKey). There is no
// allowlist in v1 (decision #22's default is a denylist).
type Policy struct {
	// ArgvDeny names options and KEY=VALUE keys whose value is masked:
	// "--password=x", "--token x", "-Ddb.password=x", "DB_PASSWORD=x".
	ArgvDeny []string
	// FlagRules deny program-specific options (see FlagRule).
	FlagRules []FlagRule
	// HeaderDeny names HTTP headers whose whole value is masked.
	HeaderDeny []string
	// QueryDeny names URL query parameters whose value is masked; "*"
	// masks every value.
	QueryDeny []string
}

// Default returns the policy every recorder uses unless configured
// otherwise. The globs are deliberately broad ("*token*" also masks a
// "--tokenizer" value): over-masking loses a value, under-masking leaks
// one. Query values are all masked — decision #22's default — since a
// query string's names say nothing reliable about what is secret.
func Default() Policy {
	return Policy{
		ArgvDeny: []string{
			"*password*", "*passwd*", "*passphrase*", "*pwd",
			"*pass", "passin", "passout", // openssl -pass/-passin
			"*secret*", "*token*", "*apikey*", "*accesskey*",
			"*privatekey*", "*credential*", "*authorization*",
			"*bearer*", "*cookie*",
		},
		FlagRules: []FlagRule{
			{Program: "mysql*", Flag: "-p"},
			{Program: "mariadb*", Flag: "-p"},
			{Program: "sshpass", Flag: "-p"},
			{Program: "redis-cli", Flag: "-a"},
			{Program: "curl", Flag: "-u"},
			{Program: "curl", Flag: "--user"},
			// docker/podman login: "run -p" is a port mapping.
			{Program: "login", Flag: "-p"},
		},
		HeaderDeny: []string{
			"authorization", "proxy-authorization", "cookie",
			"set-cookie", "*token*", "*apikey*", "*secret*",
			"*password*", "*session*", "*signature*",
		},
		QueryDeny: []string{"*"},
	}
}

// argvState is what redacting one element needs from the elements before
// it: whether it is the value of a denied option, and the program words
// seen so far, which FlagRules match against.
type argvState struct {
	maskNext bool
	progs    []string // lower-cased basenames of program words FlagRules name
}

// Argv returns argv with every secret masked; argv itself is not modified.
// The result has the same length, so element i still is argument i. An
// option that names a secret and carries no "=value" masks the element
// after it ("--token X" → "--token ***"), whatever that element looks like:
// a secret may begin with '-', and guessing wrong would leak it. An element
// holding several words ("--token x", sh -c "mysql -psecret") is redacted
// word by word with the same rules (see words).
func (p Policy) Argv(argv []string) []string {
	if argv == nil {
		return nil
	}
	out := make([]string, len(argv))
	var st argvState
	for i, arg := range argv {
		if i == 0 {
			// The program itself is a path, never an option.
			out[i] = p.Text(arg)
			p.see(&st, arg)
			continue
		}
		out[i] = p.word(&st, arg)
	}
	return out
}

// Header returns the value to record for an HTTP header: Mask when its name
// is denied, else the value with embedded URLs masked (a Referer or
// Location carries a query string and maybe userinfo).
func (p Policy) Header(name, value string) string {
	if denied(p.HeaderDeny, name) {
		return Mask
	}
	return p.Text(value)
}

// word redacts one argv element (or one word of a multi-word element) in
// sequence: it is masked whole when the word before asked for it, and it
// may ask the same of the word after it. A masked word still counts as an
// option of its own: in "--token --password x" either reading may be the
// right one, and only masking both cannot leak.
func (p Policy) word(st *argvState, arg string) string {
	masked := st.maskNext
	out, next := p.argvElem(st, arg)
	st.maskNext = next
	if masked {
		return Mask
	}
	p.see(st, arg)
	return out
}

// see records arg as a program word when it is no option and some
// FlagRule names it, once. Other words, and repeats, are not kept: a long
// shell string would otherwise make every later word scan them all.
func (p Policy) see(st *argvState, arg string) {
	if arg == "" || strings.HasPrefix(arg, "-") {
		return
	}
	prog := strings.ToLower(path.Base(arg))
	if slices.Contains(st.progs, prog) {
		return
	}
	for _, r := range p.FlagRules {
		if globMatch(strings.ToLower(r.Program), prog) {
			st.progs = append(st.progs, prog)
			return
		}
	}
}

// argvElem redacts one element and reports whether the next one is the
// value of a denied option and must be masked too. The forms are tried
// from the most specific: a program's own flag, a "Name: value" header or
// colon pair, KEY=VALUE (whose value is redacted like an element of its
// own: it may hold a header, a command or another pair), a parameter run
// ("a=b&secret=x", "jdbc:h2:file:/d;USER=u;PASSWORD=x"), an element that
// begins with a URL, several words, a bare denied option, and finally
// URLs anywhere in the text.
func (p Policy) argvElem(st *argvState, arg string) (string, bool) {
	if out, next, ok := p.flagRule(st.progs, arg); ok {
		return out, next
	}
	if out, next, ok := p.colonPair(arg); ok {
		return out, next
	}
	// A key with URL, path or space characters is not an option name: in
	// "https://h/?api_key=x" the '=' belongs to the query string. A denied
	// first key masks the whole rest, '&' and ';' included; a harmless one
	// may still start a run whose later pair is a secret
	// ("grant_type=x&client_secret=y"), which is judged pair by pair.
	if key, value, ok := strings.Cut(arg, "="); ok && !strings.ContainsAny(key, "/?&#@") && !hasSpace(key) {
		if denied(p.ArgvDeny, optionKey(key)) {
			return key + "=" + Mask, false
		}
		if isParamRun(value) {
			return p.paramRun(arg), false
		}
		out, next := p.argvElem(st, value)
		return key + "=" + out, next
	}
	if isParamRun(arg) {
		return p.paramRun(arg), false
	}
	if startsWithURL(arg) {
		return p.urlElem(st, arg)
	}
	if hasSpace(arg) {
		return p.words(st, arg)
	}
	// A bare option naming a secret takes its value from the next element.
	// "-Dname" is the exception: it sets a system property to "", and the
	// next element is an unrelated argument (often the main class).
	opt := strings.TrimLeft(arg, `'"`)
	if strings.HasPrefix(opt, "-") && !isSysProp(opt) &&
		denied(p.ArgvDeny, optionKey(opt)) {
		return arg, true
	}
	// One word: a URL in it runs to the word's end (see wordText).
	return p.wordText(arg), false
}

// isParamRun reports a run of "name=value" pairs joined by '&' or ';' —
// a form body ("user=bob&password=x") or a JDBC property list without
// "://" ("jdbc:h2:file:/data/db;USER=sa;PASSWORD=x"). A URL is not one:
// Text masks its parameters under QueryDeny, which may mask more than
// ArgvDeny does. Neither is a several-word element, which words splits
// first and whose words come back here one by one.
func isParamRun(s string) bool {
	return strings.ContainsAny(s, "&;") && strings.Contains(s, "=") &&
		!strings.Contains(s, "://") && !hasSpace(s)
}

// paramRun masks the value of every pair of a parameter run whose name
// ArgvDeny denies; separators, names and the other pairs stay, so a path
// with '&' or ';' in a file name comes back unchanged. The first pair's
// name is the whole option ("-Ddb.url=jdbc:h2:…;PASSWORD=x" yields
// "-Ddb.url", "USER", "PASSWORD"). Splitting at every '&' and ';' is what
// a form decoder and a JDBC driver do too, so a value cannot hold one.
func (p Policy) paramRun(run string) string {
	var b strings.Builder
	start := 0
	for i := 0; i <= len(run); i++ {
		if i < len(run) && run[i] != '&' && run[i] != ';' {
			continue
		}
		pair := run[start:i]
		if name, _, ok := strings.Cut(pair, "="); ok && denied(p.ArgvDeny, optionKey(name)) {
			pair = name + "=" + Mask
		}
		b.WriteString(pair)
		if i < len(run) {
			b.WriteByte(run[i])
		}
		start = i + 1
	}
	return b.String()
}

// urlElem redacts an element that begins with a URL (after an optional
// quote). The program received it as one argument, so the URL runs to
// the element's end, white space and quotes included: in
// "https://h/?q=hello world&token=x" the token is still a query value.
// What then still holds white space is redacted word by word as well, so
// "http://x --password s" cannot pass as a URL path; the masked parts
// ("***") are inert for that second pass.
func (p Policy) urlElem(st *argvState, arg string) (string, bool) {
	out := p.wordText(arg)
	if hasSpace(out) {
		return p.words(st, out)
	}
	return out, false
}

// flagRule applies the FlagRules whose program is among progs to arg. ok
// is false when no rule is about arg, so the generic handling applies.
func (p Policy) flagRule(progs []string, arg string) (out string, next, ok bool) {
	for _, r := range p.FlagRules {
		if r.Flag == "" || !slices.ContainsFunc(progs, func(prog string) bool {
			return globMatch(strings.ToLower(r.Program), prog)
		}) {
			continue
		}
		switch {
		case arg == r.Flag:
			return arg, true, true
		case strings.HasPrefix(arg, r.Flag+"="):
			return r.Flag + "=" + Mask, false, true
		case isShortFlag(r.Flag) && strings.HasPrefix(arg, r.Flag):
			return r.Flag + Mask, false, true
		}
	}
	return "", false, false
}

// optionKey strips what makes a key an option rather than a name: leading
// dashes (and quotes, for a word cut out of a shell string) and, for a Java
// system property, the 'D' of "-D". What is left is matched against the
// deny globs.
func optionKey(key string) string {
	key = strings.TrimLeft(key, `'"`)
	if isSysProp(key) {
		return key[2:]
	}
	return strings.TrimLeft(key, "-")
}

// isSysProp reports a Java system-property argument: "-Dname" or
// "-Dname=value" (one dash, capital D, a name after it).
func isSysProp(arg string) bool {
	return len(arg) > 2 && strings.HasPrefix(arg, "-D")
}

// isShortFlag reports a one-letter option such as "-p", the only kind whose
// value may be glued to it.
func isShortFlag(flag string) bool {
	return len(flag) == 2 && flag[0] == '-' && flag[1] != '-'
}

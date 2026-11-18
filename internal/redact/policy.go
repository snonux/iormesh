package redact

import (
	"path"
	"strings"
)

// Mask replaces every redacted value. It is fixed and short so a reader can
// tell a masked value from a real one, and so its length says nothing about
// the secret's.
const Mask = "***"

// FlagRule denies one option of one program, for options whose name alone
// is too generic to deny everywhere: "-p" is mysql's password but java's
// module path and ssh's port, "-u" is curl's user:password but sort's
// unique. Program is a glob over the basename of argv[0]; Flag is the
// option as written ("-p", "--user"). A short Flag ("-p") also masks a
// value glued to it ("-psecret" → "-p***").
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
		},
		HeaderDeny: []string{
			"authorization", "proxy-authorization", "cookie",
			"set-cookie", "*token*", "*apikey*", "*secret*",
			"*password*", "*session*", "*signature*",
		},
		QueryDeny: []string{"*"},
	}
}

// Argv returns argv with every secret masked; argv itself is not modified.
// The result has the same length, so element i still is argument i. An
// option that names a secret and carries no "=value" masks the element
// after it ("--token X" → "--token ***"), whatever that element looks like:
// a secret may begin with '-', and guessing wrong would leak it.
func (p Policy) Argv(argv []string) []string {
	if argv == nil {
		return nil
	}
	out := make([]string, len(argv))
	prog := ""
	if len(argv) > 0 {
		prog = path.Base(argv[0])
	}
	maskNext := false
	for i, arg := range argv {
		switch {
		case maskNext:
			// Still ask whether it is an option of its own: in
			// "--token --password x" either reading may be the right one,
			// and only masking both cannot leak.
			_, maskNext = p.argvElem(prog, arg)
			out[i] = Mask
		case i == 0:
			// The program itself is a path, never an option.
			out[i] = p.Text(arg)
		default:
			out[i], maskNext = p.argvElem(prog, arg)
		}
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

// argvElem redacts one argument after argv[0] and reports whether the next
// argument is the value of a denied option and must be masked too.
func (p Policy) argvElem(prog, arg string) (string, bool) {
	if out, next, ok := p.flagRule(prog, arg); ok {
		return out, next
	}
	// A key with URL or path characters is not an option name: in
	// "https://h/?api_key=x" the '=' belongs to the query string.
	if key, _, ok := strings.Cut(arg, "="); ok && !strings.ContainsAny(key, "/?&#@") {
		if denied(p.ArgvDeny, optionKey(key)) {
			return key + "=" + Mask, false
		}
		return p.Text(arg), false
	}
	// A bare option naming a secret takes its value from the next element.
	// "-Dname" is the exception: it sets a system property to "", and the
	// next element is an unrelated argument (often the main class).
	if strings.HasPrefix(arg, "-") && !isSysProp(arg) &&
		denied(p.ArgvDeny, optionKey(arg)) {
		return arg, true
	}
	return p.Text(arg), false
}

// flagRule applies the FlagRules that match prog to arg. ok is false when
// no rule is about arg, so the generic handling applies.
func (p Policy) flagRule(prog, arg string) (out string, next, ok bool) {
	for _, r := range p.FlagRules {
		if r.Flag == "" || !globMatch(strings.ToLower(r.Program), strings.ToLower(prog)) {
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
// dashes and, for a Java system property, the 'D' of "-D". What is left is
// matched against the deny globs.
func optionKey(key string) string {
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

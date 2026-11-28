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
//
// Kind extends the same program scoping to positional secrets (decision
// #44): a command word Verb with the words after it (redis-cli AUTH x),
// a "set <denied key> <value>" command (aws configure set), an Oracle
// logon word (sqlplus scott/x@db); see RuleKind. Such a rule leaves Flag
// empty.
type FlagRule struct {
	Program string
	Flag    string
	Kind    RuleKind
	Verb    string // a VerbArgs or VerbKeyValue rule's command word, a glob
	Args    int    // how many words after Verb a VerbArgs rule masks
}

// Policy masks secrets before anything is written. Every list is a set of
// deny globs ('*' any run, '?' one rune) matched case-insensitively against
// keys with '-', '_' and '.' removed (see normalizeKey). There is no
// allowlist in v1 (decision #22's default is a denylist).
type Policy struct {
	// ArgvDeny names options and KEY=VALUE keys whose value is masked:
	// "--password=x", "--token x", "-Ddb.password=x", "DB_PASSWORD=x".
	ArgvDeny []string
	// FlagRules deny program-specific options and positional words (see
	// FlagRule and RuleKind).
	FlagRules []FlagRule
	// HeaderDeny names HTTP headers whose whole value is masked.
	HeaderDeny []string
	// AuthSchemes names HTTP auth schemes (lower case, matched in any
	// case, not a glob): a header value starting with one and a
	// credential after it is masked whatever the header's name.
	AuthSchemes []string
	// URLHeaders names HTTP headers whose value is a URI reference, often
	// relative ("/login?token=x"), so it is masked as a URL whole: Text
	// finds only URLs with "://".
	URLHeaders []string
	// QueryDeny names URL query parameters whose value is masked; "*"
	// masks every value.
	QueryDeny []string
}

// Default returns the policy every recorder uses unless configured
// otherwise. The globs are deliberately broad ("*token*" also masks a
// "--tokenizer" value): over-masking loses a value, under-masking leaks
// one. Query values are all masked — decision #22's default — since a
// query string's names say nothing reliable about what is secret. Every
// entry has a case in testdata/corpus.tsv that needs it (task qp4):
// "*auth" for REDISCLI_AUTH, npm's _auth and httpie's --auth,
// "*accountkey*" for Azure's --account-key and AccountKey=.
func Default() Policy {
	return Policy{
		ArgvDeny: []string{
			"*password*", "*passwd*", "*passphrase*", "*pwd",
			"*pass", "passin", "passout", // openssl -pass/-passin
			"*secret*", "*token*", "*apikey*", "*accesskey*",
			"*accountkey*", "*privatekey*", "*credential*",
			"*authorization*", "*bearer*", "*cookie*", "*auth",
		},
		FlagRules:   defaultFlagRules(),
		HeaderDeny:  defaultHeaderDeny(),
		AuthSchemes: defaultAuthSchemes(),
		URLHeaders:  defaultURLHeaders(),
		QueryDeny:   []string{"*"},
	}
}

// defaultFlagRules is Default's FlagRules: the options whose name is too
// generic to deny everywhere, each for the programs where it carries a
// password (mongo* and az: -p; curl -U/--proxy-user and httpie -a:
// user:password; gh secret set -b/--body: the secret, task qp4), then the
// positional rules (see defaultPositionalRules).
func defaultFlagRules() []FlagRule {
	return append([]FlagRule{
		{Program: "mysql*", Flag: "-p"},
		{Program: "mariadb*", Flag: "-p"},
		{Program: "mongo*", Flag: "-p"},
		{Program: "az", Flag: "-p"},
		{Program: "sshpass", Flag: "-p"},
		{Program: "redis-cli", Flag: "-a"},
		{Program: "curl", Flag: "-u"},
		{Program: "curl", Flag: "--user"},
		{Program: "curl", Flag: "-U"},
		{Program: "curl", Flag: "--proxy-user"},
		{Program: "http", Flag: "-a"},
		{Program: "https", Flag: "-a"},
		// docker/podman login: "run -p" is a port mapping.
		{Program: "login", Flag: "-p"},
		// gh secret set NAME -b x.
		{Program: "secret", Flag: "-b"},
		{Program: "secret", Flag: "--body"},
	}, defaultPositionalRules()...)
}

// defaultHeaderDeny is Default's header denylist. The globs follow the
// argv ones (decision #22), written normalized (no '-') so the Java
// agent's copy, Redaction.HEADER_DENY, reads the same entry for entry —
// TestAgentPolicyMatchesDefault fails when the two drift. They match
// anywhere in a name because proxies and clouds prefix credential
// headers: X-Forwarded-Authorization, X-Amz-Credential, Cookie2. The key
// globs keep a word before "key" (api, auth, access, private,
// subscription, functions) rather than "*key*", which would mask
// Sec-WebSocket-Key and similar non-secrets. The identity globs (task
// np4) name the signed tokens and assertions auth proxies and clouds
// forward: X-JWT-Assertion (WSO2), Cf-Access-Jwt-Assertion, X-Goog-IAP-
// JWT-Assertion, X-Amzn-Oidc-Data, X-Userinfo, X-Ms-Client-Principal,
// CSRF/XSRF and OTP headers. "auth" counts only as a whole last word
// (X-Auth, X-Upstream-Auth) and "authentication" anywhere
// (Authentication-Info, Proxy-Authentication-Info; task qp4), so
// WWW-Authenticate and Proxy-Authenticate, challenges without a secret,
// stay readable. "*hmac*" names webhook signatures under another word
// (X-Shopify-Hmac-Sha256), and "*websocketprotocol*" the header
// Kubernetes clients carry a bearer token in over WebSocket
// ("base64url.bearer.authorization.k8s.io.<token>"; task qp4).
func defaultHeaderDeny() []string {
	return []string{
		"*authorization*", "*cookie*", "*bearer*", "*credential*",
		"*token*", "*secret*", "*password*", "*passwd*",
		"*passphrase*", "*pwd", "*pass", "*session*", "*signature*",
		"*api*key*", "*auth*key*", "*access*key*", "*private*key*",
		"*subscription*key*", "*functions*key*",
		"*jwt*", "*assertion*", "*oidc*", "*userinfo*", "*principal*",
		"*csrf*", "*xsrf*", "*otp", "*auth", "*authentication*",
		"*hmac*", "*websocketprotocol*",
	}
}

// defaultURLHeaders is Default's list of URL headers (normalized, the
// Java agent's Redaction.URL_HEADERS): Location and friends, plus the
// headers forward-auth proxies (nginx auth_request, Traefik, Envoy, IIS
// rewrite) copy the original request target into, query secrets and all.
func defaultURLHeaders() []string {
	return []string{
		"location", "contentlocation", "referer", "origin",
		"xoriginaluri", "xoriginalurl", "xforwardeduri",
		"xrewriteurl", "xenvoyoriginalpath",
	}
}

// argvState is what redacting one element needs from the elements before
// it: whether it is the value of a denied option, the program words seen
// so far, which FlagRules match against, and what a positional rule
// (decision #44) announced for the words after its command word.
type argvState struct {
	maskNext  bool
	progs     []string // lower-cased basenames of program words FlagRules name
	maskWords int      // words still to mask after a VerbArgs verb or a denied key
	keyWords  int      // words that may still be the key after a VerbKeyValue verb
	kvDepth   int      // KEY=VALUE levels around the current word (see keyValueElem)
	// work, when not nil, adds up the bytes argvElem is handed: the
	// deterministic step count TestArgvElemLinear holds linear (task xp4).
	work *int
}

// Argv returns argv with every secret masked; argv itself is not modified.
// The result has the same length, so element i still is argument i. An
// option that names a secret and carries no "=value" masks the element
// after it ("--token X" → "--token ***"), whatever that element looks like:
// a secret may begin with '-', and guessing wrong would leak it. An element
// holding several words ("--token x", sh -c "mysql -psecret") is redacted
// word by word with the same rules (see words). Every element then goes
// through keyValues once, which masks a denied key's value in any syntax
// the specific forms do not know (JSON, YAML, a pair inside a longer
// string); it runs here, once per element, so nested forms do not repeat
// it. A denied key left open at the element's end masks the next one.
// Last, maskSQL masks the passwords of SQL statements (psql -c "ALTER
// USER u PASSWORD 'x'"), which have no key-value separator.
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
		} else {
			out[i] = p.word(&st, arg)
		}
		var open bool
		out[i], open = p.keyValues(out[i])
		out[i] = maskSQL(out[i])
		st.maskNext = st.maskNext || open
	}
	return out
}

// Header returns the value to record for an HTTP header: Mask when its name
// is denied or its value is a credential by shape (a JWT, an auth scheme
// and its credential; see credentialValue); the value masked as one URL
// when the name is a URL header (a relative "/next?token=x" has no "://"
// for Text to find); else the value with embedded URLs masked.
func (p Policy) Header(name, value string) string {
	switch {
	case denied(p.HeaderDeny, name), p.credentialValue(value):
		return Mask
	case denied(p.URLHeaders, name):
		return p.URL(value)
	}
	return p.Text(value)
}

// word redacts one argv element (or one word of a multi-word element) in
// sequence: it is masked whole when the word before asked for it, and it
// may ask the same of the word after it. A masked word still counts as an
// option of its own: in "--token --password x" either reading may be the
// right one, and only masking both cannot leak. A positional rule's
// command word asks for the words after it the same way (see positional);
// a masked word is never a command word or a key.
func (p Policy) word(st *argvState, arg string) string {
	masked := st.maskNext || st.maskWords > 0
	if st.maskWords > 0 {
		st.maskWords--
	}
	out, next := p.argvElem(st, arg)
	st.maskNext = next
	if masked {
		return Mask
	}
	p.see(st, arg)
	p.positional(st, arg)
	return out
}

// see records arg as a program word when it is no option and some
// FlagRule names it, once. Other words, and repeats, are not kept: a long
// shell string would otherwise make every later word scan them all.
//
// Quotes around the word are dropped: in sh -c 'sudo sh -c "mysql -px"'
// the quoted run splits into `"mysql` and `-px"`, and the program is
// still mysql (task qp4).
func (p Policy) see(st *argvState, arg string) {
	arg = strings.Trim(arg, `'"`)
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
// from the most specific: a program's logon word (sqlplus scott/x@db,
// decision #44), a program's own flag, a "Name: value" header or
// colon pair, KEY=VALUE (whose value is redacted like an element of its
// own: it may hold a header, a command or another pair), an element that
// begins with a URL, several words, a bare denied option, and finally
// URLs anywhere in the text. Before KEY=VALUE, the denied pairs of a
// parameter run ("a=b&secret=x", "jdbc:h2:file:/d;USER=u;PASSWORD=x")
// are masked; the element then goes on through the other forms, so a URL
// in it is still masked as a URL.
func (p Policy) argvElem(st *argvState, arg string) (string, bool) {
	if st.work != nil {
		*st.work += len(arg)
	}
	if out, ok := p.logonRule(st.progs, arg); ok {
		return out, false
	}
	if out, next, ok := p.flagRule(st.progs, arg); ok {
		return out, next
	}
	if out, next, ok := p.colonPair(arg); ok {
		return out, next
	}
	if isParamRun(arg) {
		arg = p.paramRun(arg)
	}
	if out, next, ok := p.keyValueElem(st, arg); ok {
		return out, next
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

// maxKeyValueDepth bounds the KEY=VALUE levels of one element (see
// keyValueElem).
const maxKeyValueDepth = 64

// keyValueElem redacts a KEY=VALUE element; ok is false when arg is none.
// A key with URL, path or space characters is not an option name: in
// "https://h/?api_key=x" the '=' belongs to the query string. A denied
// key masks the whole rest, '&' and ';' included; any other key keeps
// it, and the value is redacted like an element of its own.
//
// Each level hands the rest of the element to argvElem again, whose
// forms each read all of it, so many short pairs in one element ("a=b"
// repeated) cost the square of its length (task xp4). The first
// maxKeyValueDepth levels are read as usual; a key past them keeps its
// name and has its value, the rest of the element, masked whole: an
// over-mask, never a leak, that keeps the work linear. Real elements
// nest a handful of levels; a "--set a=1,b=2,…" of helm counts one per
// pair.
func (p Policy) keyValueElem(st *argvState, arg string) (out string, next, ok bool) {
	key, value, ok := strings.Cut(arg, "=")
	if !ok || strings.ContainsAny(key, "/?&#@") || hasSpace(key) {
		return "", false, false
	}
	if st.kvDepth >= maxKeyValueDepth || denied(p.ArgvDeny, optionKey(key)) {
		return key + "=" + Mask, false, true
	}
	st.kvDepth++
	out, next = p.argvElem(st, value)
	st.kvDepth--
	return key + "=" + out, next, true
}

// isParamRun reports what may be a run of "name=value" pairs joined by
// '&' or ';' — a form body ("user=bob&password=x"), a JDBC property list
// without "://" ("jdbc:h2:file:/data/db;USER=sa;PASSWORD=x"), or a form
// body holding a raw URL ("redirect_uri=https://app/cb&client_secret=x",
// where the URL path would otherwise swallow the secret). A several-word
// element is not one: words splits it first and its words come back to
// argvElem one by one.
func isParamRun(s string) bool {
	return strings.ContainsAny(s, "&;") && strings.Contains(s, "=") && !hasSpace(s)
}

// paramRun masks the value of every pair of a parameter run whose name
// ArgvDeny denies; separators, names and the other pairs stay, so a path
// with '&' or ';' in a file name comes back unchanged. The first pair's
// name is the whole option ("-Ddb.url=jdbc:h2:…;PASSWORD=x" yields
// "-Ddb.url", "USER", "PASSWORD"). Splitting at every '&' and ';' is what
// a form decoder and a JDBC driver do too, so a value cannot hold one.
// It only adds masks: a URL query in the run is still masked under
// QueryDeny afterwards (argvElem goes on), which may mask more than
// ArgvDeny does. A masked value keeps a trailing quote, which closes a
// quoted shell word ("'a=b&secret=x'" → "'a=b&secret=***'"); a quote
// says nothing about the secret, and keeping it makes paramRun
// idempotent, which the KEY=VALUE recursion relies on.
func (p Policy) paramRun(run string) string {
	var b strings.Builder
	start := 0
	for i := 0; i <= len(run); i++ {
		if i < len(run) && run[i] != '&' && run[i] != ';' {
			continue
		}
		pair := run[start:i]
		if name, value, ok := strings.Cut(pair, "="); ok && denied(p.ArgvDeny, optionKey(name)) {
			pair = name + "=" + Mask + trailingQuote(value)
		}
		b.WriteString(pair)
		if i < len(run) {
			b.WriteByte(run[i])
		}
		start = i + 1
	}
	return b.String()
}

// trailingQuote returns the ' or " that ends s, or "".
func trailingQuote(s string) string {
	if n := len(s); n > 0 && (s[n-1] == '\'' || s[n-1] == '"') {
		return s[n-1:]
	}
	return ""
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
// Positional rules have no Flag and are skipped (see positional).
func (p Policy) flagRule(progs []string, arg string) (out string, next, ok bool) {
	for _, r := range p.FlagRules {
		if r.Kind != FlagValue || r.Flag == "" || !ruleApplies(progs, r) {
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

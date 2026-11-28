package redact

import (
	"slices"
	"strings"
)

// RuleKind says what a FlagRule masks. The zero value, FlagValue, is the
// original kind, an option's value; the others are decision #44's
// positional rules, for secrets that are bare words whose meaning comes
// only from one program's grammar. Each needs its program word before it,
// like a flag rule, so it costs nothing in other commands — there is no
// rule "a denied word masks the word after it", which would mask whatever
// follows "cat password.txt".
type RuleKind int

const (
	// FlagValue masks the value of the option Flag (see FlagRule).
	FlagValue RuleKind = iota
	// VerbArgs masks the Args words after the command word Verb, a glob
	// matched in any case: redis-cli's "AUTH [user] password", poetry's
	// and composer's "config http-basic.<repo> user password". The word
	// count tells which word is the password, and a word-by-word pass
	// cannot look ahead, so the username is masked too.
	VerbArgs
	// VerbKeyValue reads the word after the command word Verb as a key
	// and masks the word after the key when ArgvDeny denies it: "aws
	// configure set aws_secret_access_key x", "npm config set
	// //r/:_authToken x". "npm config set color false" stays. Options
	// between the verb and the key are skipped (see keyCandidate). The
	// verb counts wherever it stands after the program, so "aws s3 cp set
	// token.txt dst" masks dst: an over-mask, never a leak.
	VerbKeyValue
	// Logon masks the password of an Oracle logon word,
	// "user/password[@connect]" (sqlplus scott/x@db → scott/***@db); see
	// logonPassword for what is no logon.
	Logon
)

// defaultPositionalRules is Default's positional FlagRules (decision #44,
// task wp4). The VerbKeyValue programs are those whose "set" command
// takes credential keys the argv denylist names: aws configure set, npm,
// yarn and pnpm config set (and npm set), poetry config (no "set" word:
// poetry config pypi-token.pypi x), dotnet user-secrets set and kubectl
// config set (kubectl config set users.u.token x; "kubectl set image"
// names no denied key); poetry and composer also take HTTP basic
// credentials as two bare words. The Logon programs are the Oracle
// clients that take a logon word as an argument: sqlplus, rman and
// dgmgrl (rman target sys/x@db), and Data Pump, the classic export and
// import and SQL*Loader (expdp scott/x@db).
func defaultPositionalRules() []FlagRule {
	return []FlagRule{
		{Program: "redis-cli", Kind: VerbArgs, Verb: "auth", Args: 2},
		{Program: "poetry", Kind: VerbArgs, Verb: "http-basic.*", Args: 2},
		{Program: "composer", Kind: VerbArgs, Verb: "http-basic.*", Args: 2},
		{Program: "aws", Kind: VerbKeyValue, Verb: "set"},
		{Program: "npm", Kind: VerbKeyValue, Verb: "set"},
		{Program: "yarn", Kind: VerbKeyValue, Verb: "set"},
		{Program: "pnpm", Kind: VerbKeyValue, Verb: "set"},
		{Program: "poetry", Kind: VerbKeyValue, Verb: "config"},
		{Program: "user-secrets", Kind: VerbKeyValue, Verb: "set"},
		{Program: "kubectl", Kind: VerbKeyValue, Verb: "set"},
		{Program: "sqlplus", Kind: Logon},
		{Program: "rman", Kind: Logon},
		{Program: "dgmgrl", Kind: Logon},
		{Program: "expdp", Kind: Logon},
		{Program: "impdp", Kind: Logon},
		{Program: "exp", Kind: Logon},
		{Program: "imp", Kind: Logon},
		{Program: "sqlldr", Kind: Logon},
	}
}

// ruleApplies reports whether r's program is among progs, the program
// words seen so far (see argvState).
func ruleApplies(progs []string, r FlagRule) bool {
	program := strings.ToLower(r.Program)
	return slices.ContainsFunc(progs, func(prog string) bool {
		return globMatch(program, prog)
	})
}

// positional updates st after the unmasked word arg for the word after
// it: a word that may be the key a VerbKeyValue verb announced is judged
// as one (see keyCandidate), and a command word of a rule in scope
// announces what follows it — a key that is not denied may still be one
// ("poetry config http-basic.pypi u x"). Quotes around arg are dropped,
// as see does, for a word cut out of a nested shell string.
func (p Policy) positional(st *argvState, arg string) {
	word := strings.Trim(arg, `'"`)
	if st.keyWords > 0 {
		p.keyCandidate(st, word)
		if st.maskWords > 0 {
			return
		}
	}
	lower := strings.ToLower(word)
	for _, r := range p.FlagRules {
		if r.Verb == "" || !globMatch(strings.ToLower(r.Verb), lower) || !ruleApplies(st.progs, r) {
			continue
		}
		switch r.Kind {
		case VerbArgs:
			st.maskWords = r.Args
		case VerbKeyValue:
			st.keyWords = 1
		}
		return
	}
}

// keyCandidate judges word, which may be the key after a VerbKeyValue
// verb. An option is skipped: one with "=" ("--location=global") is
// whole, one without ("--profile", "-g") may take the next word as its
// value, so the key may be either of the next two words. Any other word
// uses up a candidate, and a denied one masks the word after it.
func (p Policy) keyCandidate(st *argvState, word string) {
	if strings.HasPrefix(word, "-") {
		if !strings.Contains(word, "=") {
			st.keyWords = 2
		}
		return
	}
	st.keyWords--
	if denied(p.ArgvDeny, optionKey(word)) {
		st.keyWords = 0
		st.maskWords = 1
	}
}

// logonRule masks the password of arg when a Logon rule is in scope and
// arg is an Oracle logon word; ok is false otherwise, so the generic
// handling applies.
func (p Policy) logonRule(progs []string, arg string) (out string, ok bool) {
	for _, r := range p.FlagRules {
		if r.Kind != Logon || !ruleApplies(progs, r) {
			continue
		}
		if from, to, found := logonPassword(arg); found {
			return arg[:from] + Mask + arg[to:], true
		}
		return "", false
	}
	return "", false
}

// logonPassword returns the [from, to) range of the password in an Oracle
// logon word, "user/password[@connect]", optionally quoted as a shell
// string leaves it ("\"scott/x@db\""). The password runs from the first
// '/' to the last '@' — one with '@' in it is written quoted
// (scott/"p@ss"@db), and an EZConnect string ("@//h:1521/svc") holds none
// — or to the word's end, a closing quote kept. No logon: a word with
// white space (words splits it first), an option, "/nolog", "/" and
// "/@db" (OS or wallet logon), "@script.sql", a KEY=VALUE word, and a
// word whose user part holds ':' (a URL, "host:/path") or '@' (a logon
// without password, "scott@//db:1521/svc").
func logonPassword(arg string) (from, to int, ok bool) {
	start := len(arg) - len(strings.TrimLeft(arg, `'"`))
	word := arg[start:]
	if word == "" || hasSpace(word) || strings.ContainsAny(word[:1], "-/@") ||
		strings.Contains(word, "=") {
		return 0, 0, false
	}
	slash := strings.IndexByte(word, '/')
	if slash <= 0 || strings.ContainsAny(word[:slash], ":@") {
		return 0, 0, false
	}
	end := len(word) - len(trailingQuote(word))
	if at := strings.LastIndexByte(word, '@'); at > slash {
		end = at
	}
	if end <= slash+1 {
		return 0, 0, false // "scott/@db": no password
	}
	return start + slash + 1, start + end, true
}

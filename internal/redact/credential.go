package redact

import "strings"

// defaultAuthSchemes is Default's AuthSchemes (lower case; the Java agent's
// Redaction.AUTH_SCHEMES, kept equal by TestAgentPolicyMatchesDefault): the
// HTTP authentication schemes whose credential follows the scheme word in
// an Authorization-style value ("Bearer <token>", "Basic <base64>").
func defaultAuthSchemes() []string {
	return []string{"bearer", "basic", "digest", "negotiate"}
}

// credentialValue reports a header value that is a credential by its shape,
// whatever the header is called (task np4): one that starts with an auth
// scheme and a credential after it, or one holding a JWT anywhere. A
// custom or vendor header name says nothing reliable, while these shapes
// do; a WWW-Authenticate challenge ("Basic realm=x") is masked too, a
// harmless over-mask.
func (p Policy) credentialValue(value string) bool {
	return p.authCredential(value) || hasJWT(value)
}

// authCredential reports a value whose first word is one of AuthSchemes
// (any case) with something after it: "Bearer abc" but not "Bearer" alone
// or "Basically fine".
func (p Policy) authCredential(value string) bool {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(value), " ")
	if !ok || strings.TrimSpace(rest) == "" {
		return false
	}
	for _, s := range p.AuthSchemes {
		if strings.EqualFold(scheme, s) {
			return true
		}
	}
	return false
}

// hasJWT reports whether value holds a JWT: a run of base64url characters
// and dots, split by any other character ("jwt=<token>; Path=/" too),
// that is a JWS (three parts, the payload not empty; an unsigned "alg:
// none" token has an empty signature) or a JWE (five parts, the
// ciphertext not empty) whose first part, the JSON header, starts with
// "eyJ" ('{"' encoded) and holds more than that.
func hasJWT(value string) bool {
	isRun := func(r rune) bool {
		return r == '.' || r == '-' || r == '_' || r >= '0' && r <= '9' ||
			r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
	}
	for _, run := range strings.FieldsFunc(value, func(r rune) bool { return !isRun(r) }) {
		if isJWT(run) {
			return true
		}
	}
	return false
}

// isJWT reports whether run, one base64url-and-dots run, is a JWS or JWE
// compact serialization (see hasJWT).
func isJWT(run string) bool {
	parts := strings.Split(run, ".")
	if len(parts[0]) <= 3 || !strings.HasPrefix(parts[0], "eyJ") {
		return false
	}
	switch len(parts) {
	case 3:
		return parts[1] != ""
	case 5:
		return parts[3] != ""
	}
	return false
}

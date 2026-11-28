package iormesh.agent.http;

import java.util.List;
import java.util.Locale;

/**
 * The agent's copy of the redaction policy for HTTP (decisions #9 and
 * #22): what an HTTP probe records passes through here before it reaches
 * the ring, so a secret never lands in the spool, not even briefly. It
 * mirrors internal/redact's defaults — the tracer applies that policy
 * again to every http event, so the two must agree only in what they
 * mask, never in what they let through:
 *
 * <ul>
 *   <li>a header whose name matches a deny glob is recorded as
 *       "name: ***": anything with authorization, cookie, bearer,
 *       credential, token, secret, password, passwd, passphrase,
 *       session or signature in it, ending in pwd or pass, or an
 *       api/auth/access/private/subscription/functions key
 *       (X-Forwarded-Authorization, Cookie2, X-Api-Key, X-Auth-Key),
 *       or naming a JWT, assertion, OIDC, userinfo, principal, CSRF/XSRF
 *       or OTP header, "auth" as its last word, "authentication" or
 *       "hmac" anywhere, or Sec-WebSocket-Protocol (X-JWT-Assertion,
 *       X-Amzn-Oidc-Data, X-Auth, Proxy-Authentication-Info,
 *       X-Shopify-Hmac-Sha256);
 *   <li>a header value that is a credential by its shape is "name: ***"
 *       under any name: a JWT anywhere in it, or a Bearer, Basic, Digest
 *       or Negotiate scheme with a credential after it;
 *   <li>every query parameter's value is masked ("?user=***&token=***",
 *       a bare "?TOKEN" too),
 *       decision #22's default, since a query string's names say
 *       nothing reliable about what is secret; ';' and '#' parameters
 *       too, as JDBC and OAuth put credentials there;
 *   <li>URL userinfo is masked wherever a URL appears (a Referer or a
 *       Location header value): "https://user:***@host/"; Location,
 *       Content-Location, Referer, Origin and the forward-auth headers
 *       X-Original-URI/-URL, X-Forwarded-URI, X-Rewrite-URL and
 *       X-Envoy-Original-Path are masked as a URL even when relative
 *       ("/next?token=***").
 * </ul>
 *
 * Path segments are recorded verbatim (#22's default; templating
 * "/orders/{id}" is a later option). Keys are matched like the Go side:
 * lower-cased, with '-', '_' and '.' removed.
 */
public final class Redaction {
    /** Replaces every masked value; the same as internal/redact.Mask. */
    public static final String MASK = "***";
    /**
     * internal/redact.Default().HeaderDeny, already normalized, entry for
     * entry (internal/redact's TestAgentPolicyMatchesDefault reads this
     * list and fails when it drifts). Globs match anywhere in a name, as
     * proxies prefix credential headers (X-Forwarded-Authorization,
     * X-Amz-Credential, Cookie2); the key globs name the word before
     * "key" so Sec-WebSocket-Key and the like stay readable. The identity
     * globs (task np4) name the signed tokens and assertions auth proxies
     * and clouds forward (X-JWT-Assertion, Cf-Access-Jwt-Assertion,
     * X-Amzn-Oidc-Data, X-Userinfo, X-Ms-Client-Principal, CSRF and OTP
     * headers); "auth" counts only as a whole last word and
     * "authentication" anywhere (Proxy-Authentication-Info, task qp4),
     * so WWW-Authenticate and Proxy-Authenticate stay readable. "*hmac*"
     * names webhook signatures (X-Shopify-Hmac-Sha256) and
     * "*websocketprotocol*" the header Kubernetes clients carry a bearer
     * token in over WebSocket (task qp4).
     */
    static final List<String> HEADER_DENY = List.of("*authorization*", "*cookie*",
            "*bearer*", "*credential*", "*token*", "*secret*", "*password*", "*passwd*",
            "*passphrase*", "*pwd", "*pass", "*session*", "*signature*", "*api*key*",
            "*auth*key*", "*access*key*", "*private*key*", "*subscription*key*",
            "*functions*key*", "*jwt*", "*assertion*", "*oidc*", "*userinfo*",
            "*principal*", "*csrf*", "*xsrf*", "*otp", "*auth", "*authentication*",
            "*hmac*", "*websocketprotocol*");

    /**
     * internal/redact.Default().AuthSchemes (checked by the same test): a
     * header value whose first word is one of these, in any case, with a
     * credential after it ("Bearer abc", "Basic dXNlcjpwYXNz") is masked
     * whatever the header is called.
     */
    static final List<String> AUTH_SCHEMES = List.of("bearer", "basic", "digest",
            "negotiate");

    /**
     * Headers whose value is a URI reference, often relative
     * ("/login?token=x"): masked as a URL whole, like
     * internal/redact.Default().URLHeaders (normalized, checked by the
     * same test). Besides Location and friends, these are the headers
     * forward-auth proxies (nginx auth_request, Traefik, Envoy, IIS
     * rewrite) copy the original request target into, query and all.
     */
    static final List<String> URL_HEADERS = List.of("location", "contentlocation",
            "referer", "origin", "xoriginaluri", "xoriginalurl", "xforwardeduri",
            "xrewriteurl", "xenvoyoriginalpath");

    private Redaction() {}

    /**
     * One header as recorded: "name: value", the name lower-cased (HTTP/1
     * names are case-insensitive, HTTP/2 lower-cases them anyway), the
     * value masked when the name is denied or the value is a credential
     * by its shape (credentialValue), else with any URL in it masked.
     */
    public static String header(String name, String value) {
        String n = name.toLowerCase(Locale.ROOT);
        String v = value == null ? "" : value;
        if (denied(n) || credentialValue(v)) {
            v = MASK;
        } else if (matches(URL_HEADERS, n)) {
            v = url(v);   // a relative reference has no "://" for text to find
        } else {
            v = text(v);
        }
        return n + ": " + v;
    }

    /** Whether a header name matches a deny glob. */
    static boolean denied(String name) {
        return matches(HEADER_DENY, name);
    }

    /**
     * Whether a header value is a credential by its shape whatever the
     * header is called (task np4), as internal/redact's credentialValue:
     * an auth scheme with a credential after it, or a JWT anywhere in it.
     */
    static boolean credentialValue(String value) {
        return authCredential(value) || hasJwt(value);
    }

    /**
     * "Bearer abc" yes; "Bearer" alone or "Basically fine" no. White space
     * (see space) before the scheme is skipped and after it ends the
     * scheme word: a TAB ("Bearer\tabc", task qp4) or a no-break space
     * (task xp4), as internal/redact's authCredential (strings.Fields).
     */
    private static boolean authCredential(String v) {
        int from = 0;
        while (from < v.length() && space(v.charAt(from))) {
            from++;
        }
        int to = from;
        while (to < v.length() && !space(v.charAt(to))) {
            to++;
        }
        int rest = to;
        while (rest < v.length() && space(v.charAt(rest))) {
            rest++;
        }
        if (rest == v.length()) {
            return false;   // one word, or nothing: no credential after a scheme
        }
        return AUTH_SCHEMES.contains(v.substring(from, to).toLowerCase(Locale.ROOT));
    }

    /**
     * Unicode white space, exactly Go's unicode.IsSpace (task xp4): the
     * ASCII blanks TAB, LF, VT, FF, CR and space, NEL, the no-break space,
     * and the White_Space characters beyond Latin-1. Character.isWhitespace
     * differs both ways — it leaves out the no-break spaces and counts the
     * separators U+001C to U+001F — so the two sides would disagree on
     * "Bearer&lt;NBSP&gt;token".
     */
    static boolean space(char c) {
        switch (c) {
            case '\t': case '\n': case 0x0B: case '\f': case '\r': case ' ':
            case 0x85: case 0xA0: case 0x1680: case 0x2028: case 0x2029:
            case 0x202F: case 0x205F: case 0x3000:
                return true;
            default:
                return c >= 0x2000 && c <= 0x200A;
        }
    }

    /**
     * Whether value holds a JWT: a run of base64url characters and dots,
     * split by any other character, that is a JWS (three parts, the
     * payload not empty) or a JWE (five parts, the ciphertext not empty)
     * whose first part starts with "eyJ" and holds more than that.
     */
    private static boolean hasJwt(String value) {
        int start = 0;
        for (int i = 0; i <= value.length(); i++) {
            if (i < value.length() && jwtChar(value.charAt(i))) {
                continue;
            }
            if (i > start && isJwt(value.substring(start, i))) {
                return true;
            }
            start = i + 1;
        }
        return false;
    }

    private static boolean jwtChar(char c) {
        return c == '.' || c == '-' || c == '_' || (c >= '0' && c <= '9')
                || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z');
    }

    /** One run is a JWS or JWE compact serialization (see hasJwt). */
    private static boolean isJwt(String run) {
        String[] parts = run.split("\\.", -1);
        if (parts[0].length() <= 3 || !parts[0].startsWith("eyJ")) {
            return false;
        }
        if (parts.length == 3) {
            return !parts[1].isEmpty();
        }
        return parts.length == 5 && !parts[3].isEmpty();
    }

    /** Whether a name, normalized, matches one of globs (also normalized). */
    private static boolean matches(List<String> globs, String name) {
        String k = normalize(name);
        for (String g : globs) {
            if (glob(g, k)) {
                return true;
            }
        }
        return false;
    }

    /**
     * A request target or URL with its userinfo and every parameter value
     * masked: "/login?user=***&token=***". Parameters start at the first
     * '?', ';' or '#' after the authority and are split at each of them
     * and '&'; a query component without '=' is a value and masked too,
     * a ';' parameter or fragment without '=' is not (see params).
     */
    public static String url(String raw) {
        if (raw == null) {
            return "";
        }
        String head = "";
        String rest = raw;
        int i = raw.indexOf("://");
        if (i >= 0) {
            int start = i + 3;
            int end = authorityEnd(raw, start);
            head = raw.substring(0, start) + userinfo(raw.substring(start, end));
            rest = raw.substring(end);
        }
        int q = firstOf(rest, "?;#");
        if (q < 0) {
            return head + rest;
        }
        return head + rest.substring(0, q) + params(rest.substring(q));
    }

    /** Masks every whitespace-separated word of s that holds a URL. */
    static String text(String s) {
        if (!s.contains("://")) {
            return s;
        }
        StringBuilder b = new StringBuilder(s.length());
        int start = 0;
        for (int i = 0; i <= s.length(); i++) {
            if (i == s.length() || Character.isWhitespace(s.charAt(i))) {
                String w = s.substring(start, i);
                b.append(w.contains("://") ? url(w) : w);
                if (i < s.length()) {
                    b.append(s.charAt(i));
                }
                start = i + 1;
            }
        }
        return b.toString();
    }

    /**
     * Where the authority starting at start ends, as internal/redact's
     * authorityEnd: an '@' before the first '/', '?' or '#' ends the
     * userinfo; without one, an '@' further on still does when the text
     * before that delimiter looks like user:password (a ':' followed by
     * something other than a port) — an unescaped '/' in a password
     * ("u:p/ss@h") would otherwise leave its tail in the path.
     */
    private static int authorityEnd(String raw, int start) {
        String rest = raw.substring(start);
        int delim = firstOf(rest, "/?#");
        if (delim < 0) {
            delim = rest.length();
        }
        int host = 0;
        int at = rest.lastIndexOf('@', delim - 1);
        if (at >= 0) {
            host = at + 1;
        } else {
            int later = rest.indexOf('@', delim);
            if (later >= 0 && userPass(rest.substring(0, delim))) {
                host = later + 1;
            }
        }
        int end = firstOf(rest.substring(host) + "/", "/?#;");
        return start + host + end;
    }

    /** "user:pass" rather than "host:port" or "host". */
    private static boolean userPass(String s) {
        int colon = s.indexOf(':');
        if (colon < 0) {
            return false;
        }
        for (int i = colon + 1; i < s.length(); i++) {
            if (s.charAt(i) < '0' || s.charAt(i) > '9') {
                return true;
            }
        }
        return false;
    }

    /** "user:pw@host" → "user:***@host"; a lone userinfo is masked whole. */
    private static String userinfo(String auth) {
        int at = auth.lastIndexOf('@');
        if (at < 0) {
            return auth;
        }
        int colon = auth.indexOf(':');
        if (colon < 0 || colon > at) {
            return MASK + auth.substring(at);
        }
        return auth.substring(0, colon + 1) + MASK + auth.substring(at);
    }

    /**
     * Masks each "name=value" of a run that starts with its separator, as
     * internal/redact's maskParams under the default "*": a query
     * component without '=' ("/reset?TOKEN", task xp4) is a value with no
     * name and masked whole; the query runs from the first '?' to the
     * fragment. A ';' parameter before the query or a fragment without
     * '=' ("/a;v1", "#section-2") is a name, and stays.
     */
    private static String params(String run) {
        StringBuilder b = new StringBuilder(run.length());
        int start = 0;
        boolean inQuery = false;
        boolean inFragment = false;
        for (int i = 0; i <= run.length(); i++) {
            if (i < run.length() && "?&;#".indexOf(run.charAt(i)) < 0) {
                continue;
            }
            b.append(param(run.substring(start, i), inQuery));
            if (i < run.length()) {
                char sep = run.charAt(i);
                b.append(sep);
                inFragment = inFragment || sep == '#';
                inQuery = !inFragment && (inQuery || sep == '?');
            }
            start = i + 1;
        }
        return b.toString();
    }

    /** One parameter masked: "name=***", or "***" for a bare query value. */
    private static String param(String p, boolean inQuery) {
        int eq = p.indexOf('=');
        if (eq >= 0) {
            return p.substring(0, eq + 1) + MASK;
        }
        return inQuery && !p.isEmpty() ? MASK : p;
    }

    private static int firstOf(String s, String chars) {
        for (int i = 0; i < s.length(); i++) {
            if (chars.indexOf(s.charAt(i)) >= 0) {
                return i;
            }
        }
        return -1;
    }

    /** Lower case without '-', '_' and '.', as internal/redact.normalizeKey. */
    static String normalize(String key) {
        StringBuilder b = new StringBuilder(key.length());
        for (char c : key.toLowerCase(Locale.ROOT).toCharArray()) {
            if (c != '-' && c != '_' && c != '.') {
                b.append(c);
            }
        }
        return b.toString();
    }

    /** '*' any run, '?' one char, else itself; backtracks to the last '*'. */
    static boolean glob(String pattern, String s) {
        int p = 0;
        int i = 0;
        int star = -1;
        int mark = 0;
        while (i < s.length()) {
            if (p < pattern.length() && pattern.charAt(p) == '*') {
                star = p++;
                mark = i;
            } else if (p < pattern.length()
                    && (pattern.charAt(p) == '?' || pattern.charAt(p) == s.charAt(i))) {
                p++;
                i++;
            } else if (star >= 0) {
                p = star + 1;
                i = ++mark;
            } else {
                return false;
            }
        }
        while (p < pattern.length() && pattern.charAt(p) == '*') {
            p++;
        }
        return p == pattern.length();
    }
}

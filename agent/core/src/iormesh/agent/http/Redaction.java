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
 *       "name: ***" (Authorization, Cookie, Set-Cookie, anything with
 *       token, apikey, secret, password, session or signature in it);
 *   <li>every query parameter's value is masked ("?user=***&token=***"),
 *       decision #22's default, since a query string's names say
 *       nothing reliable about what is secret; ';' and '#' parameters
 *       too, as JDBC and OAuth put credentials there;
 *   <li>URL userinfo is masked wherever a URL appears (a Referer or a
 *       Location header value): "https://user:***@host/"; Location,
 *       Content-Location, Referer and Origin are masked as a URL even
 *       when relative ("/next?token=***").
 * </ul>
 *
 * Path segments are recorded verbatim (#22's default; templating
 * "/orders/{id}" is a later option). Keys are matched like the Go side:
 * lower-cased, with '-', '_' and '.' removed.
 */
public final class Redaction {
    /** Replaces every masked value; the same as internal/redact.Mask. */
    public static final String MASK = "***";
    /** internal/redact.Default().HeaderDeny, already normalized. */
    static final List<String> HEADER_DENY = List.of("authorization", "proxyauthorization",
            "cookie", "setcookie", "*token*", "*apikey*", "*secret*", "*password*",
            "*session*", "*signature*");

    /**
     * Headers whose value is a URI reference, often relative
     * ("/login?token=x"): masked as a URL whole, like internal/redact's
     * URLHeader.
     */
    static final java.util.Set<String> URL_HEADERS = java.util.Set.of("location",
            "content-location", "referer", "origin");

    private Redaction() {}

    /**
     * One header as recorded: "name: value", the name lower-cased (HTTP/1
     * names are case-insensitive, HTTP/2 lower-cases them anyway), the
     * value masked when the name is denied, else with any URL in it
     * masked.
     */
    public static String header(String name, String value) {
        String n = name.toLowerCase(Locale.ROOT);
        String v = value == null ? "" : value;
        if (denied(n)) {
            v = MASK;
        } else if (URL_HEADERS.contains(n)) {
            v = url(v);   // a relative reference has no "://" for text to find
        } else {
            v = text(v);
        }
        return n + ": " + v;
    }

    /** Whether a header name matches a deny glob. */
    static boolean denied(String name) {
        String k = normalize(name);
        for (String g : HEADER_DENY) {
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
     * and '&'; a parameter without '=' has no value to mask.
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

    /** Masks each "name=value" of a run that starts with its separator. */
    private static String params(String run) {
        StringBuilder b = new StringBuilder(run.length());
        int start = 0;
        for (int i = 0; i <= run.length(); i++) {
            if (i < run.length() && "?&;#".indexOf(run.charAt(i)) < 0) {
                continue;
            }
            String p = run.substring(start, i);
            int eq = p.indexOf('=');
            b.append(eq < 0 ? p : p.substring(0, eq + 1) + MASK);
            if (i < run.length()) {
                b.append(run.charAt(i));
            }
            start = i + 1;
        }
        return b.toString();
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

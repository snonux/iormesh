package redact

import "testing"

// TestURL covers request targets and absolute URLs under the default
// policy, where every query value is masked (decision #22).
func TestURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/orders?id=42&token=x", "/orders?id=***&token=***"},
		{"/orders", "/orders"},
		// A query component without '=' is a value with no name, masked
		// whole (task xp4); a bare ';' parameter or fragment stays.
		{"/a?flag&b=", "/a?***&b=***"},
		{"/reset?tok", "/reset?***"},
		{"/x?a=1&tok&", "/x?a=***&***&"},
		{"/x?a=1;tok", "/x?a=***;***"},
		{"/x?tok#frag", "/x?***#frag"},
		{"/x#frag?tok", "/x#frag?tok"},
		{"/a;v1?tok", "/a;v1?***"},
		{"/x?", "/x?"},
		{"/a?=secret", "/a?=***"},
		{"https://h/p?x=1#y=2", "https://h/p?x=***#y=***"},
		{"https://h/cb#access_token=t&state=s", "https://h/cb#access_token=***&state=***"},
		{"https://h/doc#section-2", "https://h/doc#section-2"},
		{"https://u:p/ss@h/x", "https://u:***@h/x"},
		{"https://u:p?ss@h/x", "https://u:***@h/x"},
		{"https://u:p#s@h/x", "https://u:***@h/x"},
		{"https://h:8080/x?mail=a@b", "https://h:8080/x?mail=***"},
		{"https://u:p@h/x?mail=a@b", "https://u:***@h/x?mail=***"},
		{"https://host/@user", "https://host/@user"},
		{"https://host:8443/@user/x", "https://host:8443/@user/x"},
		{"https://user:pw@h:8443/p", "https://user:***@h:8443/p"},
		{"https://TOKEN@github.com/o/r.git", "https://***@github.com/o/r.git"},
		{"https://user:p@ss@h/", "https://user:***@h/"},
		{"https://h", "https://h"},
		{"jdbc:sqlserver://h:1433;databaseName=d;password=pw", "jdbc:sqlserver://h:1433;databaseName=***;password=***"},
		{"http://h/?q=caf%C3%A9&n=日本", "http://h/?q=***&n=***"},
		{"", ""},
	}
	for _, c := range cases {
		if got := Default().URL(c.in); got != c.want {
			t.Errorf("URL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestURLSelectiveQuery uses a named query denylist: only the named
// parameters are masked, and a percent-encoded name cannot dodge it.
func TestURLSelectiveQuery(t *testing.T) {
	p := Policy{QueryDeny: []string{"*password*", "token"}}
	cases := []struct{ in, want string }{
		{"/x?page=2&token=t&Pass%77ord=p", "/x?page=2&token=***&Pass%77ord=***"},
		{"/x?page=2", "/x?page=2"},
		{"/x?access_token=t", "/x?access_token=t"},
		{"/x?tok", "/x?tok"}, // a bare value has no name, only "*" denies it
	}
	for _, c := range cases {
		if got := p.URL(c.in); got != c.want {
			t.Errorf("URL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestText finds URLs inside free text and leaves everything else alone,
// including a '?' that is not part of a URL.
func TestText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"no url here?a=b", "no url here?a=b"},
		{"see https://u:p@h/?k=v for details", "see https://u:***@h/?k=*** for details"},
		// The comma is part of k's value as far as a URL can tell, so it
		// is masked with it; the second URL still gets its own span.
		{"a://x?k=1,b://u:p@y", "a://x?k=***b://u:***@y"},
		{`"http://u:p@h/"`, `"http://u:***@h/"`},
		{"see https://h/reset?tok now", "see https://h/reset?*** now"},
		{"://", "://"},
		{"x://", "x://"},
	}
	for _, c := range cases {
		if got := Default().Text(c.in); got != c.want {
			t.Errorf("Text(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestGlobMatch pins the matcher's rune semantics.
func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"*", "", true},
		{"*", "abc", true},
		{"a*c", "abbbc", true},
		{"a*c", "abcd", false},
		{"?", "ä", true},
		{"??", "ä", false},
		{"*password*", "dbpassword", true},
		{"*password*", "passwor", false},
		{"*pwd", "dbpwd", true},
		{"*pwd", "pwdx", false},
		{"*a*b*", "xaxxbx", true},
		{"", "", true},
		{"", "a", false},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.s); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}

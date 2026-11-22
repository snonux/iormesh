package pathtmpl

import (
	"errors"
	"strings"
	"testing"
)

// TestBuiltinFolds: numeric, canonical-UUID and long-hex segments fold
// to {id}; the query (masked already) and the fragment stay verbatim.
func TestBuiltinFolds(t *testing.T) {
	for in, want := range map[string]string{
		"/orders/123":         "/orders/{id}",
		"/orders/123/items/7": "/orders/{id}/items/{id}",
		"/users/550e8400-e29b-41d4-a716-446655440000":       "/users/{id}",
		"/users/550E8400-E29B-41D4-A716-446655440000/x":     "/users/{id}/x",
		"/blobs/0123456789abcdef":                           "/blobs/{id}",
		"/commits/9fceb02d0ae598e95dc970b74767f19372d61af8": "/commits/{id}",
		"/orders/42?token=***":                              "/orders/{id}?token=***",
		"/orders/42#frag":                                   "/orders/{id}#frag",
		"/orders/42/":                                       "/orders/{id}/",
		"/a/{id}":                                           "/a/{id}", // idempotent
	} {
		if got := Builtin(in); got != want {
			t.Errorf("Builtin(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBuiltinKeepsWords: words, versions, short hex and near-UUIDs are
// never ids — a folded word would merge routes that differ.
func TestBuiltinKeepsWords(t *testing.T) {
	for _, in := range []string{
		"/api/v2/users",
		"/api/v10/orders",
		"/",
		"",
		"/health",
		"/cafe",                    // hex letters, a word
		"/deadbeef",                // hex, shorter than 16
		"/0123456789abcde",         // 15 hex characters
		"/abcdefabcdefabcdef",      // 18 hex letters, no digit: no id
		"/0123456789abcdeg",        // not hex
		"/550e8400e29b41d4a716-44", // not a canonical UUID
		"/550e8400-e29b-41d4-a716-44665544000z",
		"/orders/1st",
		"/orders/-1",
		"/orders/1.5",
		"/a;jsessionid=***",
		"/files/report-2024",
		"/search?q=123", // the query is never folded
	} {
		if got := Builtin(in); got != in {
			t.Errorf("Builtin(%q) = %q, want it verbatim", in, got)
		}
	}
}

// TestRulesWinOverBuiltin: the first matching operator rule names the
// path — its placeholders match any one segment, its literal segments
// only themselves — and a literal rule keeps an id-like path verbatim.
func TestRulesWinOverBuiltin(t *testing.T) {
	r, err := Compile([]string{"/shops/{shop}/orders/{order}", "/status/404", "/sku/{sku}"})
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{
		"/shops/acme/orders/17":   "/shops/{shop}/orders/{order}",
		"/shops/acme/orders/17?x": "/shops/{shop}/orders/{order}?x",
		"/status/404":             "/status/404",  // pinned: no {id}
		"/status/500":             "/status/{id}", // no rule: built-in
		"/sku/ABC-1":              "/sku/{sku}",
		"/sku/ABC-1/img":          "/sku/ABC-1/img", // one segment more: no match
		"/sku/":                   "/sku/",          // a placeholder needs a segment
		"/shops/acme/orders":      "/shops/acme/orders",
		"/shops/{id}/orders/{id}": "/shops/{shop}/orders/{order}", // re-templating a folded key
	} {
		if got := r.Template(in); got != want {
			t.Errorf("Template(%q) = %q, want %q", in, got, want)
		}
	}
	var none *Rules
	if got := none.Template("/orders/9"); got != "/orders/{id}" {
		t.Errorf("nil rules: %q, want the built-in fold", got)
	}
}

// TestCompileRejects: a rule that could not be told from a path, could
// carry a control character into a terminal, or repeats another, is
// refused.
func TestCompileRejects(t *testing.T) {
	for name, rules := range map[string][]string{
		"relative":      {"orders/{id}"},
		"empty":         {""},
		"query":         {"/orders?x={id}"},
		"fragment":      {"/orders#{id}"},
		"space":         {"/my orders/{id}"},
		"control":       {"/orders/\x1b[31m"},
		"non-ascii":     {"/bestellungen/ü"},
		"bad name":      {"/orders/{a-b}"},
		"empty name":    {"/orders/{}"},
		"half brace":    {"/orders/{id"},
		"brace in word": {"/orders/x{id}"},
		"duplicate":     {"/a/{x}", "/a/{x}"},
		"too long":      {"/" + strings.Repeat("a", MaxRuleLen)},
		"too many":      make([]string, MaxRules+1),
	} {
		if _, err := Compile(rules); !errors.Is(err, ErrRule) {
			t.Errorf("%s: err %v, want ErrRule", name, err)
		}
	}
	if r, err := Compile(nil); err != nil || r != nil {
		t.Errorf("no rules: %v, %v; want nil, nil", r, err)
	}
}

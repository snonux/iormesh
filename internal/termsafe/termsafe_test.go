package termsafe

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestJSON: the runes a JSON encoder passes through but a terminal may
// act on are escaped, the value decodes to the same string, and clean
// input comes back as it is.
func TestJSON(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`{"a":"plain"}`, `{"a":"plain"}`},
		{`{"a":"grüße 日本"}`, `{"a":"grüße 日本"}`},
		{"{\"a\":\"x\u009b2J\"}", `{"a":"x\u009b2J"}`},
		{"{\"a\":\"\u202egnp.exe\"}", `{"a":"\u202egnp.exe"}`},
		{"{\"a\":\"del\x7f\"}", `{"a":"del\u007f"}`},
		{"{\"a\":\"\U000e0001tag\"}", `{"a":"\udb40\udc01tag"}`},
	} {
		got := string(JSON([]byte(tc.in)))
		if got != tc.want {
			t.Errorf("JSON(%q) = %q, want %q", tc.in, got, tc.want)
		}
		var in, out map[string]string
		if err := json.Unmarshal([]byte(tc.in), &in); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(got), &out); err != nil {
			t.Fatalf("%q does not decode: %v", got, err)
		}
		if in["a"] != out["a"] {
			t.Errorf("%q decodes to %q, the input to %q", got, out["a"], in["a"])
		}
	}
	// Invalid UTF-8 (no encoder writes it) becomes U+FFFD, as a JSON
	// decoder would read it.
	if got := string(JSON([]byte("{\"a\":\"bad\xff\"}"))); got != `{"a":"bad\ufffd"}` {
		t.Errorf("invalid UTF-8: %q", got)
	}
	clean := []byte(`{"a":"plain"}`)
	if got := JSON(clean); &got[0] != &clean[0] {
		t.Error("clean input was copied")
	}
	if bytes.Contains(JSON([]byte("{\"a\":\"\u0085\"}")), []byte("\u0085")) {
		t.Error("NEL survived")
	}
}

// TestString: printable text is unchanged, the rest quoted.
func TestString(t *testing.T) {
	if got := String("orders"); got != "orders" {
		t.Errorf("String(orders) = %q", got)
	}
	if got := String("a\x1b[2J"); got != `"a\x1b[2J"` {
		t.Errorf("String(esc) = %q", got)
	}
}

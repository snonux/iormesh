package termsafe

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
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
	for _, s := range []string{"orders", "grüße 日本", "ok \ufffd"} {
		if got := String(s); got != s {
			t.Errorf("String(%q) = %q", s, got)
		}
	}
	if got := String("a\x1b[2J"); got != `"a\x1b[2J"` {
		t.Errorf("String(esc) = %q", got)
	}
}

// TestLines (task dm4): each line is escaped on its own, the lines after
// the first indented, leading spaces kept outside the quotes; no raw
// control byte survives.
func TestLines(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"one line", "one line"},
		{"bad\x1b[2J", `"bad\x1b[2J"`},
		{"head\nlogged:\n  a\n  b\x1b]0;x\x07\n", "head\n> logged:\n>   a\n>   \"b\\x1b]0;x\\a\""},
		{"head\r\nnext", "\"head\\r\"\n> next"},
		{"a\x9bb\n\n  \u202ex", "\"a\\x9bb\"\n> \n>   \"\\u202ex\""},
	} {
		got := Lines(tc.in, "> ")
		if got != tc.want {
			t.Errorf("Lines(%q) = %q, want %q", tc.in, got, tc.want)
		}
		// 0x9b on its own: ContainsAny reads an invalid byte as U+FFFD.
		if strings.ContainsAny(got, "\x1b\x07\r\u202e") || strings.Contains(got, "\x9b") {
			t.Errorf("Lines(%q) kept a control byte: %q", tc.in, got)
		}
	}
}

// TestStringInvalidUTF8: a byte that is not UTF-8 — a raw 0x9B is the
// 8-bit CSI on terminals that honor C1 — is quoted, though a decoder
// reads it as U+FFFD, which is printable.
func TestStringInvalidUTF8(t *testing.T) {
	for _, s := range []string{"a\x9b2Jb", "a\x90b", "a\xffb"} {
		got := String(s)
		if got == s || !utf8.ValidString(got) {
			t.Errorf("String(%q) = %q, want it quoted", s, got)
		}
		if strings.Contains(got, "\x9b") {
			t.Errorf("String(%q) kept byte 0x9b", s)
		}
	}
}

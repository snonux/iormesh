package redact

import (
	"slices"
	"strings"
	"testing"
)

// TestArgvCompositesAndSubscripts pins the keyValues forms task qp4's
// corpus found leaking: a value glued from parts (a Go literal or
// conversion, a call), a Perl hash key, the name before a subscript, an
// operator run after blanks, and the program word inside a quoted run.
func TestArgvCompositesAndSubscripts(t *testing.T) {
	const s = "Zq9S3CRET"
	cases := []struct{ in, want []string }{
		{[]string{"sh", "-c", `passwords := []string{"` + s + `"}; x`},
			[]string{"sh", "-c", `passwords := ***; x`}},
		{[]string{"sh", "-c", `token := []byte("` + s + `")`},
			[]string{"sh", "-c", `token := ***`}},
		{[]string{"sh", "-c", `secrets := map[string]string{"db": "` + s + `"}`},
			[]string{"sh", "-c", `secrets := ***`}},
		{[]string{"python", "-c", `password = SecretStr("` + s + `"); a = 1`},
			[]string{"python", "-c", `password = ***; a = 1`}},
		{[]string{"perl", "-e", `$ENV{PASSWORD} = '` + s + `';`},
			[]string{"perl", "-e", `$ENV{PASSWORD} = '***';`}},
		{[]string{"sh", "-c", `token[i] = ` + s}, []string{"sh", "-c", `token[i] = ***`}},
		{[]string{"sh", "-c", `secrets["db"] = ` + s}, []string{"sh", "-c", `secrets["db"] = ***`}},
		{[]string{"ruby", "-e", `password = => ` + s}, []string{"ruby", "-e", `password = => ***`}},
		{[]string{"sh", "-c", `sudo sh -c "mysql -p` + s + `"`},
			[]string{"sh", "-c", `sudo sh -c "mysql -p***`}},
	}
	for _, c := range cases {
		got := Default().Argv(c.in)
		if !slices.Equal(got, c.want) || strings.Contains(strings.Join(got, " "), s) {
			t.Errorf("Argv(%q) =\n %q, want\n %q", c.in, got, c.want)
		}
	}
}

// TestArgvCompositesKeepBenign is the negative half: subscripts and calls
// around names that are no secret, and a subscript that is no plain
// index, change nothing.
func TestArgvCompositesKeepBenign(t *testing.T) {
	cases := [][]string{
		{"sh", "-c", `users[i] = bob; a[0] = 1`},
		{"sh", "-c", `names := []string{"a"}`},
		{"sh", "-c", `x = f(a, b)] = y`},
		{"perl", "-e", `$ENV{HOME} = '/root';`},
	}
	for _, in := range cases {
		if got := Default().Argv(in); !slices.Equal(got, in) {
			t.Errorf("Argv(%q) = %q, want unchanged", in, got)
		}
	}
}

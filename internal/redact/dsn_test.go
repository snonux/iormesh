package redact

import (
	"slices"
	"testing"
)

// TestArgvGoDSNs pins maskGoDSN (task qp4): a Go MySQL DSN without a
// scheme has its password masked wherever it stands — an option's value,
// the next element, a shell string, a header value.
func TestArgvGoDSNs(t *testing.T) {
	const s = "Zq9S3CRET"
	cases := []struct{ in, want []string }{
		{[]string{"app", "--dsn=u:" + s + "@tcp(h:3306)/db"},
			[]string{"app", "--dsn=u:***@tcp(h:3306)/db"}},
		{[]string{"app", "-dsn", "u:" + s + "@unix(/tmp/m.sock)/db?parseTime=true"},
			[]string{"app", "-dsn", "u:***@unix(/tmp/m.sock)/db?parseTime=true"}},
		{[]string{"app", "--db=u:" + s + "@/db"}, []string{"app", "--db=u:***@/db"}},
		{[]string{"app", "--db=u:p@ss" + s + "@tcp(h)/db"},
			[]string{"app", "--db=u:***@tcp(h)/db"}},
		{[]string{"sh", "-c", "run -dsn 'u:" + s + "@tcp(h)/db' x"},
			[]string{"sh", "-c", "run -dsn 'u:***@tcp(h)/db' x"}},
	}
	for _, c := range cases {
		if got := Default().Argv(c.in); !slices.Equal(got, c.want) {
			t.Errorf("Argv(%q) =\n %q, want\n %q", c.in, got, c.want)
		}
	}
	if got := Default().Header("X-Db", "u:"+s+"@tcp(h)/db"); got != "u:***@tcp(h)/db" {
		t.Errorf("Header(X-Db) = %q", got)
	}
}

// TestArgvGoDSNsKeepBenign is maskGoDSN's negative half: an image digest,
// scp's user@host:path, a DSN without a password and Oracle's "@//" are
// not taken for a DSN's password.
func TestArgvGoDSNsKeepBenign(t *testing.T) {
	cases := [][]string{
		{"docker", "pull", "nginx:1.25@sha256:0123abcd"},
		{"scp", "f", "bob@host:/tmp/x"},
		{"app", "--dsn=u:@tcp(h)/db"},
		{"app", "--dsn=u@tcp(h)/db"},
		{"sqlplus", "-L", "scott@//db:1521/svc"},
	}
	for _, in := range cases {
		if got := Default().Argv(in); !slices.Equal(got, in) {
			t.Errorf("Argv(%q) = %q, want unchanged", in, got)
		}
	}
}

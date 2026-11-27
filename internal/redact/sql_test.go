package redact

import (
	"slices"
	"testing"
)

// TestArgvSQLStatements pins maskSQL (task qp4): the password of a SQL
// statement is masked in any element, quoted, escaped-quoted inside a
// shell string, or bare after IDENTIFIED BY; the quotes stay.
func TestArgvSQLStatements(t *testing.T) {
	const s = "Zq9S3CRET"
	cases := []struct{ in, want []string }{
		{[]string{"psql", "-c", "ALTER USER u PASSWORD '" + s + "'"},
			[]string{"psql", "-c", "ALTER USER u PASSWORD '***'"}},
		{[]string{"psql", "-c", "create role u login encrypted password '" + s + "'"},
			[]string{"psql", "-c", "create role u login encrypted password '***'"}},
		{[]string{"psql", "-c", `CREATE USER "u" PASSWORD '` + s + `'`},
			[]string{"psql", "-c", `CREATE USER "u" PASSWORD '***'`}},
		{[]string{"mysql", "-e", "CREATE USER 'u'@'%' IDENTIFIED BY '" + s + "'"},
			[]string{"mysql", "-e", "CREATE USER 'u'@'%' IDENTIFIED BY '***'"}},
		{[]string{"mysql", "-e", "SET PASSWORD FOR u = PASSWORD('" + s + "')"},
			[]string{"mysql", "-e", "SET PASSWORD FOR u = PASSWORD('***')"}},
		{[]string{"sqlplus", "ALTER USER scott IDENTIFIED BY " + s + ";"},
			[]string{"sqlplus", "ALTER USER scott IDENTIFIED BY ***;"}},
		{[]string{"sh", "-c", `mysql -e "ALTER USER u PASSWORD \"` + s + `\""`},
			[]string{"sh", "-c", `mysql -e "ALTER USER u PASSWORD \"***\""`}},
		// A keyword inside a masked value starts no second value.
		{[]string{"psql", "-c", "ALTER USER u PASSWORD 'a PASSWORD '" + s},
			[]string{"psql", "-c", "ALTER USER u PASSWORD '***'" + s}},
	}
	for _, c := range cases {
		if got := Default().Argv(c.in); !slices.Equal(got, c.want) {
			t.Errorf("Argv(%q) =\n %q, want\n %q", c.in, got, c.want)
		}
	}
}

// TestArgvSQLKeepsBenign is maskSQL's negative half: prose, an unquoted
// value after PASSWORD, a keyword with no value, and a quoted key that
// keyValues masks on its own are left to the other rules.
func TestArgvSQLKeepsBenign(t *testing.T) {
	cases := [][]string{
		{"git", "commit", "-m", "password reset flow"},
		{"psql", "-c", "ALTER ROLE u PASSWORD NULL"},
		{"psql", "-c", "ALTER USER u IDENTIFIED BY"},
	}
	for _, in := range cases {
		if got := Default().Argv(in); !slices.Equal(got, in) {
			t.Errorf("Argv(%q) = %q, want unchanged", in, got)
		}
	}
	// A quoted key's name with blanks inside is keyValues', not SQL; a
	// quoted word after PASSWORD in prose is an over-mask, never a leak.
	for in, want := range map[string]string{
		`{" password " : "x"}`:          `{" password " : "***"}`,
		`{\" password \":\"x\"}`:        `{\" password \":\"***\"}`,
		`forgot your password 'again'?`: `forgot your password '***'?`,
		`say " password 'x'`:            `say " password '***'`,
		`say " password "`:              `say " password "***`,
	} {
		if got := Default().Argv([]string{"app", in}); got[1] != want {
			t.Errorf("Argv(%q) = %q, want %q", in, got[1], want)
		}
	}
}

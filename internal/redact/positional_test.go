package redact

import (
	"slices"
	"testing"
)

// TestArgvPositionalRules pins what decision #44's positional rules mask,
// word for word: the corpus only checks that the canary is gone, this
// checks that nothing else goes with it.
func TestArgvPositionalRules(t *testing.T) {
	cases := []struct{ in, want []string }{
		{[]string{"redis-cli", "AUTH", "pw"}, []string{"redis-cli", "AUTH", "***"}},
		{[]string{"redis-cli", "-h", "h", "auth", "u", "pw"},
			[]string{"redis-cli", "-h", "h", "auth", "***", "***"}},
		{[]string{"sh", "-c", "redis-cli AUTH pw && echo ok"},
			[]string{"sh", "-c", "redis-cli AUTH *** *** echo ok"}},
		{[]string{"aws", "configure", "set", "aws_secret_access_key", "pw"},
			[]string{"aws", "configure", "set", "aws_secret_access_key", "***"}},
		{[]string{"npm", "config", "set", "//r/:_authToken", "pw"},
			[]string{"npm", "config", "set", "//r/:_authToken", "***"}},
		{[]string{"sqlplus", "scott/pw@orcl", "@a.sql"}, []string{"sqlplus", "scott/***@orcl", "@a.sql"}},
		{[]string{"sqlplus", "scott/pw"}, []string{"sqlplus", "scott/***"}},
		{[]string{"sqlplus", `scott/"p@ss"@//db:1521/svc`}, []string{"sqlplus", "scott/***@//db:1521/svc"}},
		{[]string{"sh", "-c", `sqlplus -s "scott/pw" @a.sql`},
			[]string{"sh", "-c", `sqlplus -s "scott/***" @a.sql`}},
		// Options before the key are skipped, with a value or without.
		{[]string{"aws", "configure", "set", "--profile", "p", "aws_session_token", "pw"},
			[]string{"aws", "configure", "set", "--profile", "p", "aws_session_token", "***"}},
		{[]string{"npm", "config", "set", "-g", "--location=global", "_auth", "pw"},
			[]string{"npm", "config", "set", "-g", "--location=global", "_auth", "***"}},
		{[]string{"poetry", "config", "http-basic.pypi", "u", "pw"},
			[]string{"poetry", "config", "http-basic.pypi", "***", "***"}},
		// Documented over-masks: any "set" after aws, a script's path
		// argument after sqlplus.
		{[]string{"aws", "s3", "cp", "set", "token.txt", "s3://b/"},
			[]string{"aws", "s3", "cp", "set", "token.txt", "***"}},
		{[]string{"sqlplus", "u/pw@db", "@run.sql", "in/data.csv"},
			[]string{"sqlplus", "u/***@db", "@run.sql", "in/***"}},
	}
	for _, c := range cases {
		if got := Default().Argv(c.in); !slices.Equal(got, c.want) {
			t.Errorf("Argv(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestArgvPositionalKeepsBenignArgs is the negative half: a positional
// rule needs its program and its command word, and a "set" rule a denied
// key, so these come back byte for byte. "cat password.txt" is why there
// is no generic "a denied word masks the next" rule (decision #44).
func TestArgvPositionalKeepsBenignArgs(t *testing.T) {
	cases := [][]string{
		{"redis-cli", "GET", "auth"},
		{"cat", "password.txt", "notes.txt"},
		{"grep", "-r", "auth", "src"}, // AUTH outside redis-cli
		{"aws", "configure", "get", "region"},
		{"aws", "configure", "set", "region", "eu-west-1"},
		{"npm", "config", "set", "color", "false"},
		{"npm", "config", "set", "-g", "color", "false"},
		{"aws", "configure", "set", "--profile", "p", "region", "eu-west-1"},
		{"poetry", "config", "virtualenvs.in-project", "true"},
		{"git", "config", "set", "user.name", "bob"}, // no rule for git
		{"sqlplus", "/nolog"},
		{"sqlplus", "/", "as", "sysdba"},
		{"sqlplus", "/@orcl"},
		{"sqlplus", "@script.sql"},
		{"sqlplus", "-S", "scott@orcl"},
		{"sqlplus", "scott/@orcl"},
		{"cp", "a/b", "c/d"}, // a logon only under sqlplus
	}
	for _, in := range cases {
		if got := Default().Argv(in); !slices.Equal(got, in) {
			t.Errorf("Argv(%q) = %q, want it unchanged", in, got)
		}
	}
}

package redact

import (
	"slices"
	"strings"
	"testing"
)

// TestArgvMasksSecrets covers the option shapes decision #9 names
// (-Dpassword=…, --token …) and their relatives. Each case lists the whole
// argv so the masked element's neighbours are checked too.
func TestArgvMasksSecrets(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"sysprop", []string{"java", "-Dpassword=hunter2", "Main"},
			[]string{"java", "-Dpassword=***", "Main"}},
		{"sysprop dotted", []string{"java", "-Djavax.net.ssl.keyStorePassword=s3cr3t", "-jar", "a.jar"},
			[]string{"java", "-Djavax.net.ssl.keyStorePassword=***", "-jar", "a.jar"}},
		{"long eq", []string{"app", "--password=hunter2"}, []string{"app", "--password=***"}},
		{"long next", []string{"app", "--token", "abc123", "run"},
			[]string{"app", "--token", "***", "run"}},
		{"single dash next", []string{"app", "-secret", "x"}, []string{"app", "-secret", "***"}},
		{"next looks like option", []string{"app", "--token", "-abc"},
			[]string{"app", "--token", "***"}},
		{"api key separators", []string{"app", "--api-key=k", "--API_KEY=k", "--apiKey", "k"},
			[]string{"app", "--api-key=***", "--API_KEY=***", "--apiKey", "***"}},
		{"case insensitive", []string{"app", "--PassWord=x", "-DDB.PASSWORD=y"},
			[]string{"app", "--PassWord=***", "-DDB.PASSWORD=***"}},
		{"env style", []string{"env", "DB_PASSWORD=x", "AWS_SECRET_ACCESS_KEY=y", "LANG=C", "app"},
			[]string{"env", "DB_PASSWORD=***", "AWS_SECRET_ACCESS_KEY=***", "LANG=C", "app"}},
		{"value with equals", []string{"app", "--token=a=b=c"}, []string{"app", "--token=***"}},
		{"empty value", []string{"app", "--password="}, []string{"app", "--password=***"}},
		{"unicode value", []string{"app", "--password=pässwörd✓"}, []string{"app", "--password=***"}},
		{"unicode key", []string{"app", "--Passwort-PASSWORD=x"}, []string{"app", "--Passwort-PASSWORD=***"}},
		{"url userinfo", []string{"git", "clone", "https://bob:tok@github.com/x.git"},
			[]string{"git", "clone", "https://bob:***@github.com/x.git"}},
		{"url query", []string{"curl", "https://h/p?api_key=k&page=2#frag"},
			[]string{"curl", "https://h/p?api_key=***&page=***#frag"}},
		{"jdbc in sysprop", []string{"java", "-Ddb.url=jdbc:postgresql://app:pw@db:5432/x?ssl=true", "M"},
			[]string{"java", "-Ddb.url=jdbc:postgresql://app:***@db:5432/x?ssl=***", "M"}},
		{"secret is last", []string{"app", "--token"}, []string{"app", "--token"}},
		{"double masked next", []string{"app", "--token", "--password", "x"},
			[]string{"app", "--token", "***", "***"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Default().Argv(c.in); !slices.Equal(got, c.want) {
				t.Errorf("Argv(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestArgvKeepsBenignArgs is the negative half: arguments that name no
// secret come back byte for byte, including look-alikes of the denied
// shapes.
func TestArgvKeepsBenignArgs(t *testing.T) {
	cases := [][]string{
		{"java", "-Xmx512m", "-XX:+UseG1GC", "-cp", "/opt/a.jar:/opt/b.jar", "com.example.Main", "--port", "8080"},
		{"java", "-Dpassword", "com.example.Main"}, // -Dname sets "", next is the main class
		{"java", "-p", "/opt/mods", "-m", "app/com.example.Main"},
		{"ssh", "-p", "22", "user@host"},
		{"mkdir", "-p", "/tmp/a/b"},
		{"sort", "-u", "file"},
		{"app", "--user=alice", "--key", "/etc/key.pem", "LANG=C"},
		{"app", "what?", "*.txt", "a=b", "=", ""},
		{"app", "https://example.com/path/to", "http://h:8080/"},
		{"grüße", "--größe=10", "日本語"},
		{},
	}
	for _, in := range cases {
		if got := Default().Argv(in); !slices.Equal(got, in) {
			t.Errorf("Argv(%q) = %q, want unchanged", in, got)
		}
	}
	if Default().Argv(nil) != nil {
		t.Error("Argv(nil) is not nil")
	}
}

// TestArgvFlagRules checks the program-scoped options: "-p" is a password
// for mysql but not for java or ssh (see TestArgvKeepsBenignArgs).
func TestArgvFlagRules(t *testing.T) {
	cases := []struct{ in, want []string }{
		{[]string{"/usr/bin/mysql", "-u", "root", "-phunter2", "db"},
			[]string{"/usr/bin/mysql", "-u", "root", "-p***", "db"}},
		{[]string{"mysqldump", "-p", "hunter2"}, []string{"mysqldump", "-p", "***"}},
		{[]string{"sshpass", "-p", "hunter2", "ssh", "h"}, []string{"sshpass", "-p", "***", "ssh", "h"}},
		{[]string{"redis-cli", "-a", "pw", "ping"}, []string{"redis-cli", "-a", "***", "ping"}},
		{[]string{"curl", "-u", "bob:pw", "http://h/"}, []string{"curl", "-u", "***", "http://h/"}},
		{[]string{"curl", "--user=bob:pw"}, []string{"curl", "--user=***"}},
		{[]string{"CURL", "-ubob:pw"}, []string{"CURL", "-u***"}},
	}
	for _, c := range cases {
		if got := Default().Argv(c.in); !slices.Equal(got, c.want) {
			t.Errorf("Argv(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestArgvDoesNotModifyInput guards the caller's slice: the exec record's
// argv is still needed unredacted for pattern matching (decision #23).
func TestArgvDoesNotModifyInput(t *testing.T) {
	in := []string{"java", "-Dpassword=hunter2", "--token", "t"}
	keep := slices.Clone(in)
	_ = Default().Argv(in)
	if !slices.Equal(in, keep) {
		t.Errorf("input modified: %q", in)
	}
}

// TestArgvNoSecretSurvives plants secrets in every recognized shape and
// checks that none of them is anywhere in the output, which is the
// property the trace contract needs (docs/milestones.md M0 HTTP scenarios).
func TestArgvNoSecretSurvives(t *testing.T) {
	const s = "hunter2"
	in := []string{
		"java", "-Dpassword=" + s, "-Dapp.db.password=" + s, "--token", s,
		"--client-secret=" + s, "API_TOKEN=" + s, "https://u:" + s + "@h/?q=" + s,
		"-Ddb=jdbc:sqlserver://h;user=u;password=" + s, "--passphrase", s,
	}
	out := strings.Join(Default().Argv(in), " ")
	if strings.Contains(out, s) {
		t.Errorf("secret survived: %s", out)
	}
}

// TestHeader checks the HTTP header half of the policy M3 builds on.
func TestHeader(t *testing.T) {
	cases := []struct{ name, value, want string }{
		{"Authorization", "Bearer abc", Mask},
		{"proxy-authorization", "Basic abc", Mask},
		{"Cookie", "s=1", Mask},
		{"Set-Cookie", "s=1; Path=/", Mask},
		{"X-Auth-Token", "abc", Mask},
		{"X-API-Key", "abc", Mask},
		{"Content-Type", "application/json", "application/json"},
		{"Referer", "https://u:p@h/x?code=abc", "https://u:***@h/x?code=***"},
		{"Accept", "", ""},
	}
	for _, c := range cases {
		if got := Default().Header(c.name, c.value); got != c.want {
			t.Errorf("Header(%q, %q) = %q, want %q", c.name, c.value, got, c.want)
		}
	}
}

// TestHeaderDeniesCredentialHeaders is task 0j4's leak: the header
// denylist named "authorization" and "cookie" exactly, so prefixed and
// API-key style credential headers were recorded verbatim.
func TestHeaderDeniesCredentialHeaders(t *testing.T) {
	for _, name := range []string{
		"X-Authorization", "X-Forwarded-Authorization",
		"X-Original-Authorization", "X-Amzn-Remapped-Authorization",
		"Cookie2", "X-Auth-Key", "X-Access-Key", "X-Amz-Credential",
		"Ocp-Apim-Subscription-Key", "X-Functions-Key", "Private-Key",
		"X-Passwd", "X-Api-Key", "Api-Key", "X-Goog-Api-Key",
		"X-Bearer", "X-Passphrase",
	} {
		if got := Default().Header(name, "S3CRET"); got != Mask {
			t.Errorf("Header(%q) = %q, want %q", name, got, Mask)
		}
	}
}

// TestHeaderKeepsOrdinaryHeaders guards against the widened globs
// over-masking: headers that carry no credential stay readable.
func TestHeaderKeepsOrdinaryHeaders(t *testing.T) {
	for _, name := range []string{
		"Content-Type", "Accept", "User-Agent", "Host", "X-Request-Id",
		"Keep-Alive", "Cache-Control", "X-Forwarded-For",
		"X-Forwarded-Proto", "Sec-WebSocket-Key", "Content-Length",
	} {
		if got := Default().Header(name, "plain"); got != "plain" {
			t.Errorf("Header(%q, \"plain\") = %q, want it unmasked", name, got)
		}
	}
}

// TestHeaderMasksURLHeaders: a header holding a URI reference is masked
// as a URL even when relative. Forward-auth proxies (nginx auth_request,
// Traefik, Envoy) copy the original request target, query secrets and
// all, into X-Original-URI and the like; "/login?token=x" has no "://"
// for Text to find.
func TestHeaderMasksURLHeaders(t *testing.T) {
	cases := []struct{ name, value, want string }{
		{"Location", "/next?token=s", "/next?token=***"},
		{"Content-Location", "/a?b=s", "/a?b=***"},
		{"Referer", "/r?code=s", "/r?code=***"},
		{"X-Original-URI", "/login?token=s", "/login?token=***"},
		{"X-Original-URL", "/a?b=s", "/a?b=***"},
		{"X-Forwarded-URI", "/reset?code=s&user=bob", "/reset?code=***&user=***"},
		{"X-Rewrite-URL", "/a?b=s", "/a?b=***"},
		{"X-Envoy-Original-Path", "/a?b=s", "/a?b=***"},
		{"X-Forwarded-Prefix", "/api", "/api"},
	}
	for _, c := range cases {
		if got := Default().Header(c.name, c.value); got != c.want {
			t.Errorf("Header(%q, %q) = %q, want %q", c.name, c.value, got, c.want)
		}
	}
}

// FuzzArgv checks the invariants on arbitrary input: no panic, the same
// number of elements, and a planted --password value never survives.
func FuzzArgv(f *testing.F) {
	f.Add("java", "-Dx=y", "https://u:p@h/?a=b")
	f.Add("", "=", "://")
	f.Add("mysql", "-p", "\xff\xfe")
	f.Add("app", "x", "--token")
	f.Fuzz(func(t *testing.T, a, b, c string) {
		in := []string{a, b, c, "--password", "Zq9planted"}
		out := Default().Argv(in)
		if len(out) != len(in) {
			t.Fatalf("len %d, want %d", len(out), len(in))
		}
		if out[4] != Mask {
			t.Fatalf("planted value survived: %q", out)
		}
	})
}

// TestArgvMultiWordElements is review round 1's first leak: a name and its
// value in one element, and shell command strings, are redacted word by
// word with the same rules, separators kept.
func TestArgvMultiWordElements(t *testing.T) {
	cases := []struct{ in, want []string }{
		{[]string{"app", "--token abc123"}, []string{"app", "--token ***"}},
		{[]string{"app", "--token\tsecret"}, []string{"app", "--token\t***"}},
		{[]string{"sh", "-c", "app --token x"}, []string{"sh", "-c", "app --token ***"}},
		{[]string{"sh", "-c", "mysql -psecret db"}, []string{"sh", "-c", "mysql -p*** db"}},
		{[]string{"sh", "-c", "java -Dx=1 -Dpassword=s M"}, []string{"sh", "-c", "java -Dx=1 -Dpassword=*** M"}},
		{[]string{"sh", "-c", "a  --password  s  b"}, []string{"sh", "-c", "a  --password  ***  b"}},
		{[]string{"sh", "-c", "app --token"}, []string{"sh", "-c", "app --token"}},
		{[]string{"sh", "-c", "app --token", "s"}, []string{"sh", "-c", "app --token", "***"}},
		{[]string{"app", "--opts=--token s"}, []string{"app", "--opts=--token ***"}},
		{[]string{"sh", "-c", `curl -H 'Authorization: Bearer s' http://h/`},
			[]string{"sh", "-c", `curl -H 'Authorization: *** http://h/`}},
		{[]string{"sh", "-c", "x 'a b' --secret 'c d'"}, []string{"sh", "-c", "x 'a b' --secret ***"}},
		{[]string{"app", "a b c"}, []string{"app", "a b c"}},
		{[]string{"app", "　--token　s"}, []string{"app", "　--token　***"}}, // ideographic spaces
	}
	for _, c := range cases {
		if got := Default().Argv(c.in); !slices.Equal(got, c.want) {
			t.Errorf("Argv(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestArgvHeaderAndColonPairs is review round 1's second leak: curl -H
// "Authorization: Bearer s", --header=…, and colon pairs.
func TestArgvHeaderAndColonPairs(t *testing.T) {
	cases := []struct{ in, want []string }{
		{[]string{"curl", "-H", "Authorization: Bearer s", "http://h/"},
			[]string{"curl", "-H", "Authorization: ***", "http://h/"}},
		{[]string{"curl", "--header=Authorization: Bearer s"}, []string{"curl", "--header=Authorization: ***"}},
		{[]string{"curl", "-H", "Cookie:a=s"}, []string{"curl", "-H", "Cookie:***"}},
		{[]string{"curl", "-H", "X-Api-Key:", "s"}, []string{"curl", "-H", "X-Api-Key:", "***"}},
		{[]string{"app", "--api_key:s"}, []string{"app", "--api_key:***"}},
		{[]string{"openssl", "enc", "-pass", "pass:s"}, []string{"openssl", "enc", "-pass", "***"}},
		{[]string{"openssl", "rsa", "-passin", "pass:s"}, []string{"openssl", "rsa", "-passin", "***"}},
		{[]string{"x", "pass:s"}, []string{"x", "pass:***"}},
		{[]string{"curl", "-H", "Accept: text/plain"}, []string{"curl", "-H", "Accept: text/plain"}},
		{[]string{"java", "-XX:+UseG1GC", "-Xlog:gc:file=/tmp/gc", "jdbc:h2:mem", "host:8080"},
			[]string{"java", "-XX:+UseG1GC", "-Xlog:gc:file=/tmp/gc", "jdbc:h2:mem", "host:8080"}},
	}
	for _, c := range cases {
		if got := Default().Argv(c.in); !slices.Equal(got, c.want) {
			t.Errorf("Argv(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestArgvWrappedPrograms: FlagRules see the program a wrapper runs, and
// a subcommand ("docker login"), not just argv[0].
func TestArgvWrappedPrograms(t *testing.T) {
	cases := []struct{ in, want []string }{
		{[]string{"sudo", "-u", "root", "mysql", "-psecret"}, []string{"sudo", "-u", "root", "mysql", "-p***"}},
		{[]string{"env", "HOME=/x", "mysql", "-p", "s"}, []string{"env", "HOME=/x", "mysql", "-p", "***"}},
		{[]string{"timeout", "10", "redis-cli", "-a", "s"}, []string{"timeout", "10", "redis-cli", "-a", "***"}},
		{[]string{"docker", "login", "-p", "s", "reg"}, []string{"docker", "login", "-p", "***", "reg"}},
		{[]string{"docker", "run", "-p", "8080:80", "img"}, []string{"docker", "run", "-p", "8080:80", "img"}},
		{[]string{"app", "--pass", "s", "--dbpass=s"}, []string{"app", "--pass", "***", "--dbpass=***"}},
	}
	for _, c := range cases {
		if got := Default().Argv(c.in); !slices.Equal(got, c.want) {
			t.Errorf("Argv(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// FuzzArgvOneElement: whatever precedes it, a "--password value" pair
// inside one element never lets the value through.
func FuzzArgvOneElement(f *testing.F) {
	f.Add("sh -c")
	f.Add("'")
	f.Add(`a="b`)
	f.Add("Accept:")
	f.Fuzz(func(t *testing.T, prefix string) {
		in := []string{"x", prefix + " --password Zq9planted"}
		if out := strings.Join(Default().Argv(in), " "); strings.Contains(out, "Zq9planted") {
			t.Fatalf("planted value survived: %q", out)
		}
	})
}

// TestArgvMultiWordHeaderValue: inside a multi-word element a denied
// header's value is the rest of the element, which can itself be several
// words; the whole of it is masked, not just its first word.
func TestArgvMultiWordHeaderValue(t *testing.T) {
	p := Default()
	cases := []struct{ in, want, leak string }{
		{"-H Authorization: Bearer S", "-H Authorization: ***", "S"},
		{"  Authorization: Bearer S", "  Authorization: ***", "S"},
		{"-H Proxy-Authorization:\tBasic dXNlcjpwYXNz", "-H Proxy-Authorization:\t***", "dXNlcjpwYXNz"},
		{"-H Accept: text/plain", "-H Accept: text/plain", ""}, // not denied
	}
	for _, c := range cases {
		got := p.Argv([]string{"curl", c.in})[1]
		if got != c.want || (c.leak != "" && strings.Contains(got, c.leak)) {
			t.Errorf("Argv(curl, %q)[1] = %q, want %q", c.in, got, c.want)
		}
	}
	// A denied header name as the last word still masks the next element.
	if got := p.Argv([]string{"curl", "-H Authorization:", "Bearer S"}); got[2] != Mask {
		t.Errorf("trailing header name: %q, want the next element masked", got)
	}
}

// TestArgvParamRunsAndSchemelessURLs is task zi4's leak: a secret in a
// parameter run whose first pair is harmless (a form body, a JDBC
// property list without "://"), Oracle's thin user/password@, and a URL
// whose query holds a space or a quote inside one argv element.
func TestArgvParamRunsAndSchemelessURLs(t *testing.T) {
	const s = "S3CRET"
	cases := []struct{ in, want []string }{
		{[]string{"curl", "-d", "grant_type=client_credentials&client_secret=" + s},
			[]string{"curl", "-d", "grant_type=client_credentials&client_secret=***"}},
		{[]string{"curl", "--data", "user=bob&password=" + s},
			[]string{"curl", "--data", "user=bob&password=***"}},
		{[]string{"curl", "--data", "a&password=" + s},
			[]string{"curl", "--data", "a&password=***"}},
		{[]string{"sh", "-c", "curl -d 'a=b&secret=" + s + "' http://h"},
			[]string{"sh", "-c", "curl -d 'a=b&secret=***' http://h"}},
		{[]string{"curl", "-d", "grant_type=authorization_code&redirect_uri=https://app/cb&client_secret=" + s},
			[]string{"curl", "-d", "grant_type=authorization_code&redirect_uri=https://app/cb&client_secret=***"}},
		{[]string{"curl", "-d", "redirect_uri=https://app/cb&client_secret=" + s},
			[]string{"curl", "-d", "redirect_uri=https://app/cb&client_secret=***"}},
		// A run inside a URL query still gets every query value masked.
		{[]string{"java", "-Durl=https://u:" + s + "@h/?a=b&page=" + s},
			[]string{"java", "-Durl=https://u:***@h/?a=***&page=***"}},
		{[]string{"java", "-Dspring.datasource.url=jdbc:h2:file:/data/db;USER=sa;PASSWORD=" + s, "M"},
			[]string{"java", "-Dspring.datasource.url=jdbc:h2:file:/data/db;USER=sa;PASSWORD=***", "M"}},
		{[]string{"app", "jdbc:h2:file:/data/db;PASSWORD=" + s},
			[]string{"app", "jdbc:h2:file:/data/db;PASSWORD=***"}},
		{[]string{"java", "-Ddb.url=jdbc:oracle:thin:scott/" + s + "@//db:1521/svc"},
			[]string{"java", "-Ddb.url=jdbc:oracle:thin:scott/***@//db:1521/svc"}},
		{[]string{"java", "-Ddb.url=JDBC:Oracle:oci:scott/" + s + "@db"},
			[]string{"java", "-Ddb.url=JDBC:Oracle:oci:scott/***@db"}},
		{[]string{"curl", "https://h/?q=hello world&token=" + s},
			[]string{"curl", "https://h/?q=***&token=***"}},
		{[]string{"curl", "https://h/x?token=abc'" + s},
			[]string{"curl", "https://h/x?token=***"}},
		{[]string{"curl", "http://x --password " + s},
			[]string{"curl", "http://x --password ***"}},
	}
	for _, c := range cases {
		got := Default().Argv(c.in)
		if !slices.Equal(got, c.want) || strings.Contains(strings.Join(got, " "), s) {
			t.Errorf("Argv(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestArgvParamRunsKeepBenignArgs is zi4's negative half: runs and URLs
// that name no secret come back byte for byte.
func TestArgvParamRunsKeepBenignArgs(t *testing.T) {
	cases := [][]string{
		{"cp", "/data/a&b.txt", "/data/x;y=1.txt", "/data/r&d=1.txt"},
		{"java", "-Dfoo=bar", "-Dlist=a&b", "-Dcp=a.jar;b.jar", "M"},
		{"curl", "-d", "a=b&c=d", "https://example.com/a/b", "http://h/x'y"},
		{"java", "-Ddb.url=jdbc:h2:file:/data/db;USER=sa;MODE=PostgreSQL"},
		{"java", "-Ddb.url=jdbc:oracle:thin:@//db:1521/svc", "jdbc:oracle:thin:scott@db"},
		{"sh", "-c", "curl 'https://example.com/a' && echo a=b&c=d"},
	}
	for _, in := range cases {
		if got := Default().Argv(in); !slices.Equal(got, in) {
			t.Errorf("Argv(%q) = %q, want unchanged", in, got)
		}
	}
}

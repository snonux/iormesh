package redact

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// planted is the secret the tests below plant; it names no key and holds
// no separator, so only a leak can bring it into the output.
const planted = "Zq9S3CRET"

// TestArgvJSONValues is task rm4's leak: a denied key inside a JSON-style
// value (Spring's --spring.application.json, a curl body, a kubectl patch)
// was passed unmasked because no specific form knew the syntax. The value
// must be gone and the key and structure kept.
func TestArgvJSONValues(t *testing.T) {
	const s = planted
	cases := []struct{ in, want []string }{
		{[]string{"java", "-jar", "app.jar", `--spring.application.json={"spring":{"datasource":{"password":"` + s + `"}}}`},
			[]string{"java", "-jar", "app.jar", `--spring.application.json={"spring":{"datasource":{"password":"***"}}}`}},
		{[]string{"java", `-Dspring.application.json={"spring.datasource.password":"` + s + `"}`, "-jar", "app.jar"},
			[]string{"java", `-Dspring.application.json={"spring.datasource.password":"***"}`, "-jar", "app.jar"}},
		{[]string{"curl", "-d", `{"username":"bob","password":"` + s + `"}`, "https://h/login"},
			[]string{"curl", "-d", `{"username":"bob","password":"***"}`, "https://h/login"}},
		{[]string{"curl", "--data", `{"client_secret": "` + s + `"}`, "https://h/token"},
			[]string{"curl", "--data", `{"client_secret": "***"}`, "https://h/token"}},
		{[]string{"sh", "-c", `curl -d '{"api_key":"` + s + `"}' https://h/x`},
			[]string{"sh", "-c", `curl -d '{"api_key":"***"}' https://h/x`}},
		{[]string{"kubectl", "patch", "secret", "x", "-p", `{"stringData":{"token":"` + s + `"}}`},
			[]string{"kubectl", "patch", "secret", "x", "-p", `{"stringData":{"token":"***"}}`}},
		{[]string{"app", `--config={'password': '` + s + `'}`},
			[]string{"app", `--config={'password': '***'}`}},
		{[]string{"app", `{\"password\":\"` + s + `\"}`},
			[]string{"app", `{\"password\":\"***\"}`}},
		{[]string{"app", `{"password": "a \"q\" ` + s + `"}`},
			[]string{"app", `{"password": "***"}`}},
		// A denied key's object or array value is masked whole (review
		// round 1): its inner keys need not be denied themselves.
		{[]string{"curl", "-d", `{"user":"a","api_tokens":["` + s + `"]}`},
			[]string{"curl", "-d", `{"user":"a","api_tokens":***}`}},
		// A mismatched closer must not end the value early (rm4 review
		// round 2): the stray "]" makes the value malformed; all is masked.
		{[]string{"app", `{"password":{"a":"x"]},"` + s + `"]}`},
			[]string{"app", `{"password":***`}},
		{[]string{"app", `{"tokens":["` + s + `","` + s + `2"],"n":1}`},
			[]string{"app", `{"tokens":***,"n":1}`}},
		{[]string{"sh", "-c", `curl -d "{\"password\": [\"` + s + `\"]}" http://h`},
			[]string{"sh", "-c", `curl -d "{\"password\": ***}" http://h`}},
		{[]string{"app", `{"secrets":{"db":"` + s + `","x":"}]"}}`},
			[]string{"app", `{"secrets":***}`}},
		{[]string{"app", `{"password":{"value":"` + s + `"`},
			[]string{"app", `{"password":***`}},
		{[]string{"app", `{password: ab\` + s + `}`},
			[]string{"app", `{password: ***}`}},
		{[]string{"app", `{"password":\` + s + `}`},
			[]string{"app", `{"password":***}`}},
		{[]string{"app", `{" password ":"` + s + `"}`},
			[]string{"app", `{" password ":"***"}`}},
		{[]string{"curl", "-d", `{"password":`, `"` + s + `"}`},
			[]string{"curl", "-d", `{"password":`, Mask}},
		{[]string{"app", "/login?token=" + s + "&x=1"},
			[]string{"app", "/login?token=***&x=1"}},
		{[]string{"sh", "-c", "export PASSWORD=" + s + "; run"},
			// A denied KEY= masks its whole word, the ';' included.
			[]string{"sh", "-c", "export PASSWORD=*** run"}},
	}
	for _, c := range cases {
		got := Default().Argv(c.in)
		if !slices.Equal(got, c.want) || strings.Contains(strings.Join(got, " "), s) {
			t.Errorf("Argv(%q) =\n %q, want\n %q", c.in, got, c.want)
		}
	}
}

// corpusKeys is one key per ArgvDeny glob family.
var corpusKeys = []string{
	"password", "pwd", "pass", "secret", "token", "apikey", "accesskey",
	"privatekey", "credential", "authorization", "bearer", "cookie",
}

// corpusSyntaxes is every container syntax a key-value pair is known to
// arrive in, as argv tails; K is the key, V the value. A new syntax is one
// more row here, not one more audit finding.
var corpusSyntaxes = [][]string{
	{"--K", "V"},
	{"--K=V"},
	{"K=V"},
	{"-DK=V"},
	{"-Dapp.K=V"},
	{"K:V"},
	{"K: V"},
	{"--opts", "K: V"},
	{"a=b&K=V"},
	{"x;K=V"},
	{"/p?K=V&x=1"},
	{"export K=V; run"},
	{`{"K":"V"}`},
	{`{"K": "V"}`},
	{`{"K" : "V", "n": 1}`},
	{`{"a":{"b":{"K":"V"}}}`},
	{`{"user":"bob","K":"V"}`},
	{`{'K': 'V'}`},
	{`{\"K\":\"V\"}`},
	{`{"K":V}`},
	{`[{"K":"V"}]`},
	// No plurals here: "*pwd" and "*pass" deny suffixes only.
	{`{"user":"a","api_K":["V"]}`},
	{`{"K":["V1","V2"]}`},
	{`{"K":{"db":"V"}}`},
	{`{"K":{"value":"V"}}`},
	{`{"K":[{"a":"]"},"V"]}`},
	{`{"K":["V"`},
	{`{K: ab\V}`},
	{`{"K":\V}`},
	{`{" K ":"V"}`},
	{"sh", "-c", `curl -d "{\"K\": [\"V\"]}" http://h`},
	{`{"K":`, `"V"}`},
	{`{"K": `, `"V"}`},
	{"K: V\nother: 1"},
	{"--json={\"K\":\"V\"}"},
	{"OPTS={\"K\":\"V\"}"},
	{"sh", "-c", `curl -d '{"K":"V"}' http://h`},
	{"sh", "-c", `curl -d "{\"K\": \"V\"}" http://h`},
	{"sh", "-c", "app --K=V && echo K: V"},
	{`h = {"K" => "V"}`},
	{`h = {"K"=>"V"}`},
	{"K := V"},
	{`os.environ["K"] = "V"`},
	{"sh", "-c", `run --K V\ V x`},
	{"sh", "-c", `export K=V\ V; run`},
}

// TestArgvKeyValueCorpus plants a value under every denied key family in
// every syntax of corpusSyntaxes and fails when it survives anywhere in
// the output, or when an element is gained or lost.
func TestArgvKeyValueCorpus(t *testing.T) {
	for _, key := range corpusKeys {
		for _, syn := range corpusSyntaxes {
			in := []string{"app"}
			for _, e := range syn {
				e = strings.ReplaceAll(e, "K", key)
				in = append(in, strings.ReplaceAll(e, "V", planted))
			}
			out := Default().Argv(in)
			if len(out) != len(in) || strings.Contains(strings.Join(out, "\x00"), planted) {
				t.Errorf("Argv(%q) = %q: planted value survived", in, out)
			}
		}
	}
}

// TestArgvKeyValueKeepsBenign is the negative half: values under keys that
// name no secret, the key words in prose without a separator, and numbers
// come back byte for byte.
func TestArgvKeyValueKeepsBenign(t *testing.T) {
	cases := [][]string{
		{"curl", "-d", `{"username":"bob","page":3}`, "https://h/login"},
		{"echo", "the password is required", "token reset done"},
		{"app", `--spring.application.json={"server":{"port":8080}}`},
		{"app", "ratio=3:4", "--port=8080", "12:30", "1,2,3"},
		{"sh", "-c", `curl -d '{"name":"x","n":1}' http://h`},
		{"app", `{\"user\":\"bob\"}`, `{'mode': 'fast'}`},
		{"app", "--password-stdin"},
	}
	for _, in := range cases {
		if got := Default().Argv(in); !slices.Equal(got, in) {
			t.Errorf("Argv(%q) = %q, want unchanged", in, got)
		}
	}
	// A non-denied key next to a denied one keeps its value.
	got := Default().Argv([]string{"curl", "-d", `{"username":"bob","password":"x"}`})
	if !strings.Contains(got[2], `"username":"bob"`) {
		t.Errorf("username lost: %q", got)
	}
}

// TestArgvOperatorsSubscriptsEscapes is task op4's leak: an operator run
// after the separator ("=>", ":=", "=="), a key in a subscript or call
// before it (os.environ["PASSWORD"] = …) and a backslash-escaped blank
// inside a value (a shell keeps "SEK\ RET" one word) each let the secret
// through. The value is masked; key, operator and structure stay.
func TestArgvOperatorsSubscriptsEscapes(t *testing.T) {
	const s = planted
	cases := []struct{ in, want []string }{
		{[]string{"ruby", "-e", `h = {"password" => "` + s + `"}`},
			[]string{"ruby", "-e", `h = {"password" => "***"}`}},
		// One word with '=' is a KEY=VALUE first: a denied key masks the
		// rest of the word, closers included (over-masking, no leak).
		{[]string{"ruby", "-e", `h = {"password"=>"` + s + `"}`},
			[]string{"ruby", "-e", `h = {"password"=***`}},
		{[]string{"php", "-r", `$a = ['password' => '` + s + `'];`},
			[]string{"php", "-r", `$a = ['password' => '***'];`}},
		{[]string{"perl", "-e", `my %h = (password => "` + s + `");`},
			[]string{"perl", "-e", `my %h = (password => "***");`}},
		{[]string{"app", "password := " + s}, []string{"app", "password := ***"}},
		{[]string{"app", "token == " + s}, []string{"app", "token == ***"}},
		// A run glued to the value may be part of it: masked with it.
		{[]string{"app", "{password: :=" + s + "}"}, []string{"app", "{password: ***}"}},
		{[]string{"app", "{password:>" + s + "}"}, []string{"app", "{password:***}"}},
		{[]string{"app", "{password:=" + s + "}"}, []string{"app", "{password:***"}},
		{[]string{"python", "-c", `os.environ["PASSWORD"] = "` + s + `"`},
			[]string{"python", "-c", `os.environ["PASSWORD"] = "***"`}},
		{[]string{"python", "-c", `d['api_token']='` + s + `'`},
			[]string{"python", "-c", `d['api_token']=***`}},
		{[]string{"python", "-c", `d['api_token'] = '` + s + `'`},
			[]string{"python", "-c", `d['api_token'] = '***'`}},
		{[]string{"app", `getenv(secret) = ` + s}, []string{"app", `getenv(secret) = ***`}},
		{[]string{"sh", "-c", `run --password ` + s + `\ ` + s + ` x`},
			[]string{"sh", "-c", `run --password *** x`}},
		{[]string{"sh", "-c", `export PASSWORD=` + s + `\ ` + s + `; run`},
			[]string{"sh", "-c", `export PASSWORD=*** run`}},
		{[]string{"app", `{password: ` + s + `\ ` + s + `}`},
			[]string{"app", `{password: ***}`}},
	}
	for _, c := range cases {
		got := Default().Argv(c.in)
		if !slices.Equal(got, c.want) || strings.Contains(strings.Join(got, " "), s) {
			t.Errorf("Argv(%q) =\n %q, want\n %q", c.in, got, c.want)
		}
	}
}

// TestArgvOperatorsKeepBenign is op4's negative half: operators, subscripts
// and escaped blanks around keys that name no secret change nothing.
func TestArgvOperatorsKeepBenign(t *testing.T) {
	cases := [][]string{
		{"ruby", "-e", `h = {"user" => "bob"}`},
		{"sh", "-c", "test a => b"},
		{"sh", "-c", "[ x == y ] && z := 1"},
		{"python", "-c", `d["user"] = "bob"; a[0] = 1`},
		{"app", "f(x) = y", "a[0]=1"},
		{"sh", "-c", `cp my\ file /tmp`},
		{"sh", "-c", `run --user my\ name x`},
	}
	for _, in := range cases {
		if got := Default().Argv(in); !slices.Equal(got, in) {
			t.Errorf("Argv(%q) = %q, want unchanged", in, got)
		}
	}
}

// FuzzArgvKeyValue: whatever surrounds it, a JSON pair under a denied key
// never lets its value through, and the length stays.
func FuzzArgvKeyValue(f *testing.F) {
	f.Add("", "")
	f.Add(`{"a":`, `}`)
	f.Add(`sh -c '`, `'`)
	f.Add(`\`, `\"`)
	f.Add("--x=", ",")
	f.Add(`{"a":["]`, `]}`)
	f.Add(`{"tokens":{"x":`, `}}`)
	f.Fuzz(func(t *testing.T, prefix, suffix string) {
		in := []string{"x", prefix + `"password":"` + planted + `"` + suffix}
		out := Default().Argv(in)
		if len(out) != len(in) || strings.Contains(strings.Join(out, " "), planted) {
			t.Fatalf("planted value survived: %q", out)
		}
	})
}

// TestArgvKeyValueLinear guards the work bound: a long element full of
// separators and denied keys is redacted in one pass, not one per pair.
// The budget is generous (a race build is slow); a quadratic scan of
// 1.6 MiB takes minutes.
func TestArgvKeyValueLinear(t *testing.T) {
	big := strings.Repeat(`"password":"x",a:b=c `, 1<<16) + strings.Repeat(" :", 1<<18)
	start := time.Now()
	_ = Default().Argv([]string{"x", big})
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("Argv on %d bytes took %v", len(big), d)
	}
}

// BenchmarkArgvKeyValue measures the key-value pass on a 1 MiB element.
func BenchmarkArgvKeyValue(b *testing.B) {
	big := strings.Repeat(`{"password":"x","n":1} `, 1<<15)
	for range b.N {
		_ = Default().Argv([]string{"x", big})
	}
}

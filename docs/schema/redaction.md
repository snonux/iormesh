# Redaction

Part of [Trace schema (v1, frozen)](../schema.md).

One policy, `internal/redact` (`redact.Default()`), masks secrets
before an event exists — no queue, sink or later stage holds the
raw value (decisions.md #9 and #22). A masked value is `***`
and the structure stays:

| Input | Recorded |
|-------|----------|
| `-Dpassword=s`, `--client-secret=s`, `DB_PASSWORD=s` | `-Dpassword=***`, `--client-secret=***`, `DB_PASSWORD=***` |
| `--token s`, `-secret s` (option without `=`) | `--token ***` — the next element is masked, whatever it is |
| `mysql -ps`, `curl -u bob:s`, `redis-cli -a s` | `-p***`, `-u ***`, `-a ***` — only for those programs |
| `https://bob:s@h/p?k=s` (anywhere in an argument) | `https://bob:***@h/p?k=***` |
| `jdbc:sqlserver://h;user=u;password=s` | `jdbc:sqlserver://h;user=***;password=***` |
| `https://h/cb#access_token=s` | `https://h/cb#access_token=***` — fragment parameters too |
| `-d grant_type=x&client_secret=s`, `jdbc:h2:file:/d;USER=u;PASSWORD=s` | `client_secret=***`, `PASSWORD=***` — a `&`/`;` run is judged pair by pair, the other pairs stay |
| `jdbc:oracle:thin:scott/s@//db:1521/svc` | `jdbc:oracle:thin:scott/***@//db:1521/svc` |
| `https://h/?q=a b&token=s`, `https://h/?token=a's` (one argument) | `https://h/?q=***&token=***`, `https://h/?token=***` — a URL starting an argument runs to its end |
| `--token s` in one element; `sh -c "mysql -ps"` | `--token ***`; `mysql -p***` — several words are redacted word by word, separators and quoting kept |
| `curl -H "Authorization: Bearer s"`, `--header=Cookie: s`, `--api_key:s`, `pass:s` | `Authorization: ***`, `--header=Cookie: ***`, `--api_key:***`, `pass:***` |
| `sudo -u root mysql -ps`, `env X=1 mysql -p s`, `docker login -p s` | `-p***`, `-p ***`, `-p ***` — a flag rule applies after any word naming its program |
| `--spring.application.json={"db":{"password":"s"}}`, `-d '{"token": "s"}'`, `{'secret': 's'}`, `{\"pwd\":\"s\"}`, `{"tokens":["s"]}`, `/p?token=s&a=1` | `{"db":{"password":"***"}}`, `{"token": "***"}`, `{'secret': '***'}`, `{\"pwd\":\"***\"}`, `{"tokens":***}`, `/p?token=***&a=1` — any denied key, then `:` or `=`, then a value, in any element; `{"password":` as one element masks the next |
| `ruby -e 'h = {"password" => "s"}'`, `password := s`, `os.environ["PASSWORD"] = "s"`, `sh -c 'run --password a\ s'` | `{"password" => "***"}`, `password := ***`, `os.environ["PASSWORD"] = "***"`, `run --password ***` — an operator run after the separator, a key in a subscript, a backslash-escaped blank in a value |
| `$ENV{PASSWORD} = 's'`, `token[i] = s`, `passwords := []string{"s"}`, `token := []byte("s")` | `$ENV{PASSWORD} = '***'`, `token[i] = ***`, `passwords := ***`, `token := ***` — a Perl hash key, the name before a subscript, a value glued from parts (task qp4) |
| `psql -c "ALTER USER u PASSWORD 's'"`, `mysql -e "CREATE USER u IDENTIFIED BY 's'"`, `SET PASSWORD FOR u = PASSWORD('s')`, `IDENTIFIED BY s` | `PASSWORD '***'`, `IDENTIFIED BY '***'`, `PASSWORD('***')`, `IDENTIFIED BY ***` — SQL statements, in any element (task qp4) |
| `--dsn=u:s@tcp(h:3306)/db`, `u:s@unix(/sock)/db`, `u:s@/db` | `u:***@tcp(h:3306)/db`, … — Go MySQL DSNs without a scheme (task qp4) |
| `redis-cli AUTH s`, `redis-cli auth u s`, `aws configure set aws_secret_access_key s`, `npm config set //r/:_authToken s`, `sqlplus scott/s@db` | `AUTH ***`, `auth *** ***`, `aws_secret_access_key ***`, `//r/:_authToken ***`, `scott/***@db` — positional rules of one program, also in shell strings (decision #44, task wp4) |

Keys match case-insensitively with `-`, `_` and `.` ignored, so
one glob covers every spelling. The argv denylist names
`password`, `passwd`, `passphrase`, `pwd` and `pass` (suffixes),
`passin` and `passout` (openssl), `auth` (a suffix:
`REDISCLI_AUTH`, npm's `_auth`, httpie's `--auth`), `secret`,
`token`, `apikey`, `accesskey`, `accountkey` (Azure),
`privatekey`, `credential`, `authorization`, `bearer` and
`cookie` anywhere in a key. The program-scoped flags are
`-p` of `mysql*`, `mariadb*`, `mongo*`, `az`, `sshpass` and
`docker`/`podman login`, `redis-cli -a`, curl's `-u`, `--user`,
`-U` and `--proxy-user`, httpie's `-a` and `gh secret set`'s
`-b`/`--body`. The same program scoping holds the positional
rules (decision #44): `redis-cli`'s `AUTH` (any case) masks the
two words after it, the password and, in `AUTH user pass`, the
user; `set` after `aws`, `npm`, `yarn` or `pnpm` (`aws
configure set`, `npm config set`, `npm set`), `config` after
`poetry` and `set` after `dotnet user-secrets` read the next
word as a key, options before it skipped (`--profile p`,
`--location=global`, `-g`), and mask the word after it when the
argv denylist denies the key (`config set color false` stays);
`http-basic.<repo>` after `poetry config` or `composer config`
masks the user and password after it; and
an Oracle logon word of `sqlplus`, `user/pass` or
`user/pass@connect`, has its password masked up to the last
`@` (`/nolog`, `/`, `/@db`, `@script.sql` and `user@db` are no
logon). URL userinfo is always masked, query values all
(decision #22's default). After the specific forms above, one
generic pass looks at every argv element for a denied key
(optionally quoted or escaped-quoted, optionally inside a
subscript, call or Perl hash: `os.environ["PASSWORD"]`,
`getenv(TOKEN)`, `$ENV{TOKEN}`, or before a subscript:
`token[i]`), optional blanks, `:` or `=` with the rest of an
operator run (`=>` of Ruby, PHP and Perl, `:=`, `==`, also after
blanks: `password = => s`), optional blanks and a value — a
quoted string up to its matching unescaped quote, or an
unquoted value: bare runs (up to white space, a quote or one of
`,;&})]`) and bracketed groups (`{…}`, `[…]`, `(…)` up to the
matching bracket) glued together, so `[]string{"s"}` and
`SecretStr("s")` are masked whole — and masks only the value, so JSON,
YAML, dicts, hashes, assignments and pairs inside shell strings
need no parser of their own (`{"password" => "s"}` →
`{"password" => "***"}`); the pass is linear in the element's
length. An object or array under a denied key is masked whole
(`"secrets":***`, `"tokens":***`); under any other key its pairs
are judged one by one. A blank escaped by a backslash stays in
its word and its value, as a shell keeps it (`--password a\ s`
→ `--password ***`). Two forms have no key-value separator and
are masked by their keywords, whatever the policy's lists: SQL
statements (`[ENCRYPTED] PASSWORD 's'`, `IDENTIFIED [WITH
plugin] BY 's'`, `SET PASSWORD FOR u = 's'`, `PASSWORD('s')`,
Oracle's bare `IDENTIFIED BY s`; after `PASSWORD` only a quoted
value counts, so prose stays) and Go MySQL DSNs without a scheme
(`u:s@tcp(h)/db`, `u:s@/db`: the `@` must be followed by
`proto(` or one `/`, so `image:tag@sha256:…` stays).

It is best effort; what it does not recognize is listed under
[Accepted limits](#accepted-limits). A quoted value with spaces
inside a shell string is masked up to the quote's end, as a
shell would split it. Over-masked, never leaked: an operator
run glued to a bare value (`password:>s`) is masked with it; a
one-word `KEY=VALUE` under a denied key loses the rest of the
word (`{"password"=>"s"}` → `{"password"=***`); a blank after an
even run of backslashes, which a shell does split at, still
counts as part of the value; `--password-stdin`, `--no-auth`,
`--oauth` and `--auth=none` mask the argument or value after
them; a quoted word after `PASSWORD`, or a word after
`IDENTIFIED BY`, in prose is masked as a SQL value (an unclosed
quote after it masks to the end of the string); `host:x@/path`
shapes read as a Go MySQL DSN; `mongod`/`mongos -p PORT` and
`kubectl get secret -b x` mask the flag's value; under
`redis-cli` the two words after any `auth` are masked, the user
of `AUTH user pass`, the next word in a shell string
(`redis-cli AUTH s && x` → `*** ***`) and a key's value
(`redis-cli SET auth v` → `***`) too, and so are the user and
password after `http-basic.<repo>`; any `set` word after `aws`,
`npm`, `yarn` or `pnpm` counts as the verb, so `aws s3 cp set
token.txt s3://b/` masks `s3://b/`, and after an option without
`=` either of the next two words may be the key; a relative path
with a `/` after `sqlplus` in the same command loses its part
after the first `/` (a script argument, `sqlplus u/p@db @run.sql
in/data.csv` → `in/***`, or `sqlplus … && cp a/b c`); and the
whole
`Sec-WebSocket-Protocol` header and `X-Hmac-*` headers are
masked, ordinary subprotocols included. HTTP headers (M3) use the
same package:
names containing `authorization`, `cookie`, `bearer`,
`credential`, `token`, `secret`, `password`, `passwd`,
`passphrase`, `session` or `signature`, names ending in `pwd` or
`pass`, and api, auth, access, private, subscription and
functions keys (`*api*key*`, `*auth*key*`, …) are recorded as
`***` — so `X-Forwarded-Authorization`, `Cookie2`,
`X-Amz-Credential`, `X-Api-Key` and `Ocp-Apim-Subscription-Key`
are too. So are the identity headers auth proxies and clouds
forward: names containing `jwt`, `assertion`, `oidc`,
`userinfo`, `principal`, `csrf`, `xsrf`, `authentication` or
`hmac`, or ending in `otp` or in the word `auth`
(`X-JWT-Assertion`, `Cf-Access-Jwt-Assertion`,
`X-Amzn-Oidc-Data`, `X-Ms-Client-Principal`, `X-Auth`,
`Proxy-Authentication-Info`, `X-Shopify-Hmac-Sha256`), and
`Sec-WebSocket-Protocol`, where Kubernetes clients carry a
bearer token (`base64url.bearer.authorization.k8s.io.<token>`).
`Sec-WebSocket-Key`, `Keep-Alive`, `WWW-Authenticate`,
`Proxy-Authenticate` or `X-Forwarded-For` stay. A value is
masked by its shape under any name: one holding a JWT (three
or five dot-separated base64url parts, the first starting
`eyJ`) or starting with `Bearer`, `Basic`, `Digest` or
`Negotiate`, any white space and a credential (a `WWW-Authenticate`
challenge such as `Basic realm=x` is masked too, an
over-mask). URLs in the other headers' values and
every query value of the path are masked; `location`,
`content-location`, `referer`, `origin` and the forward-auth
headers `x-original-uri`, `x-original-url`, `x-forwarded-uri`,
`x-rewrite-url` and `x-envoy-original-path` are masked as a URL
even when relative (`/next?token=***`).
The Java agent applies a copy of these header and query rules
(`iormesh.agent.http.Redaction`) before a request reaches the
spool, so a secret never lands on disk, and the tracer applies
`internal/redact` again to every http event it writes.

## The corpus

`internal/redact/testdata/corpus.tsv` (task qp4) holds the real
secret carriers the rules above are held to: options and
environment, program flags, curl/wget/httpie/git, database
clients, JDBC and Go DSNs, SQL, containers, Kubernetes and
clouds, shell strings, Ruby/PHP/Perl/Python/Go/JS one-liners,
the credential headers of proxies and identity providers, and
URLs. Each case plants the canary `SEKRET` and says `masked` or
`limit:<tag>`. `TestCorpus` runs every case through
`Policy.Argv`, `Policy.Header` or `Policy.URL`, and the Java
agent's `CorpusTest` runs the header and url cases through
`Redaction`: a masked case fails when the canary survives, a
limit case when it disappears — a limit that got fixed becomes
a masked case and leaves the table below in the same change.
`TestCorpusLimitsDocumented` fails when the table's tags and the
corpus's differ. A new leak is a new corpus line first, then
the rule that masks it.

## Accepted limits

What the denylist does not recognize, each proven by a corpus
case that keeps its canary:

| Limit | Example | Why it stays |
|-------|---------|--------------|
| `stdin` | `echo s \| docker login --password-stdin`, `echo "AUTH s" \| redis-cli` | the secret is an argument of `echo`; only the pipe to a program reading it from stdin marks it, and a positional rule (decision #44) is scoped to the program that takes the word |
| `positional` | `sqlplus scott/"x s"@db`, `rman target sys/s@db`, `expdp scott/s@db`, `kubectl config set users.u.token s` | decision #44's rules cover only the programs they name, and a logon word split at a quoted blank loses its shape; another program's grammar needs a rule of its own |
| `innocuous-name` | `--db s`, `{"db":"s"}`, `X-Custom: s`, `X-Custom: token=s` | the name says nothing; masking every value would hide all arguments and headers |
| `no-separator` | `<password>s</password>`, `<wsse:Password>s</wsse:Password>`, `password -> s`, `password s` | no `:` or `=` between key and value (XML, an arrow, prose); a parser per syntax is out of scope |
| `call-argument` | `define('DB_PASSWORD', 's')`, `os.environ.setdefault("TOKEN", "s")`, `System.setProperty("db.password", "s")`, `db.auth("u", "s")` | key and value are separate arguments of a call; which argument is secret depends on the function |
| `split-element` | `password` `:` `s` as three elements, `{"password"` then `:"s"}`, `{"password":["x` then `s"]}` | the pair is split across argv elements; only an open separator at an element's end carries over |
| `json-escape` | `{"pass\u0077ord":"s"}` | the key is written with JSON `\u` escapes, which the scan does not decode |
| `path-segment` | `/reset/s`, `https://hooks.slack.com/services/T/B/s`, `https://api.telegram.org/bots/…` | path segments are recorded verbatim (decision #22: templated at the controller's aggregations, raw in the events) |

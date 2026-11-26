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

Keys match case-insensitively with `-`, `_` and `.` ignored, so
one glob covers every spelling. The argv denylist names
`password`, `passwd`, `passphrase`, `pwd` and `pass` (suffixes),
`passin` and `passout` (openssl), `secret`,
`token`, `apikey`, `accesskey`, `privatekey`, `credential`,
`authorization`, `bearer` and `cookie` anywhere in a key; URL
userinfo is always masked, query values all (decision #22's
default). After the specific forms above, one generic pass looks
at every argv element for a denied key (optionally quoted or
escaped-quoted), optional blanks, `:` or `=`, optional blanks
and a value — a quoted string up to its matching unescaped
quote, an object or array up to its matching bracket, or a bare
run up to white space, a quote or one of `,;&})]` — and masks
only the value, so JSON, YAML, dicts and pairs inside shell
strings need no parser of their own; the pass is linear in the
element's length. An object or array under a denied key is
masked whole (`"secrets":***`, `"tokens":***`); under any other
key its pairs are judged one by one. It is best effort: a
secret under an innocuous name (also in JSON, `{"db":"s"}`) or
as a bare positional argument is not recognized; neither is a
pair without `:` or `=` (XML, `<password>s</password>`), a key
written with JSON `\u` escapes, or a pair whose key is split
across argv elements or whose separator starts the next element
(`"password"` then `:"s"`); a quoted
value with spaces inside a shell string is masked only up to the
quote's end as a shell would split it, and the
defaults over-mask rather than leak (`--password-stdin` masks the
argument after it). HTTP headers (M3) use the same package:
names containing `authorization`, `cookie`, `bearer`,
`credential`, `token`, `secret`, `password`, `passwd`,
`passphrase`, `session` or `signature`, names ending in `pwd` or
`pass`, and api, auth, access, private, subscription and
functions keys (`*api*key*`, `*auth*key*`, …) are recorded as
`***` — so `X-Forwarded-Authorization`, `Cookie2`,
`X-Amz-Credential`, `X-Api-Key` and `Ocp-Apim-Subscription-Key`
are too, while `Sec-WebSocket-Key`, `Keep-Alive` or
`X-Forwarded-For` stay. URLs in the other headers' values and
every query value of the path are masked; `location`,
`content-location`, `referer`, `origin` and the forward-auth
headers `x-original-uri`, `x-original-url`, `x-forwarded-uri`,
`x-rewrite-url` and `x-envoy-original-path` are masked as a URL
even when relative (`/next?token=***`).
The Java agent applies a copy of these header and query rules
(`iormesh.agent.http.Redaction`) before a request reaches the
spool, so a secret never lands on disk, and the tracer applies
`internal/redact` again to every http event it writes.

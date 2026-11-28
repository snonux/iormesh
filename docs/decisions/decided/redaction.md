# Decided — redaction

Part of [Decided](../decided.md), in [Decisions](../../decisions.md).

The redaction policy itself is open decision #9 (argv and env) and
#22 (HTTP), both built on their defaults in
[open.md](../open.md); what is settled about it lives here.

- **#44 Positional secrets: per-program rules** — decided
  (owner, 2026-11-27, task wp4), under #9 and #22. Some secrets
  are bare words whose meaning comes only from one program's
  grammar: `redis-cli AUTH [user] s`, `aws configure set
  aws_secret_access_key s`, `npm config set //r/:_authToken s`,
  `sqlplus scott/s@db`. Task qp4's corpus kept them as the
  `positional` limit. *Decided: a small set of program-specific
  positional rules, kinds of the existing program-scoped
  `FlagRule`*:
  - a command word of a program masks the words after it:
    redis-cli's `AUTH`, any case, one or two arguments, so both
    words are masked — the count says which is the password,
    and a word-by-word pass cannot look ahead; poetry's and
    composer's `config http-basic.<repo> user pass` the same
    way;
  - a `config set`-style command word masks the value after its
    key only when the argv denylist denies that key (aws
    `configure set`, npm/yarn/pnpm `config set` and `npm set`,
    `poetry config`, `dotnet user-secrets set`, `kubectl config
    set`, amended 2026-11-28); options before
    the key are skipped (`--profile p`, `--location=global`,
    `-g`). The rule fires on any `set` word after the program,
    not only after `configure` or `config`: `aws s3 cp set
    token.txt s3://b/` masks `s3://b/`, an over-mask;
  - an Oracle logon word `user/password[@connect]` of sqlplus
    — and, amended 2026-11-28, of rman, dgmgrl, expdp, impdp,
    exp, imp and sqlldr, which take the same word (`rman target
    sys/s@db`, `expdp scott/s@db`) — has its password masked (`/nolog`, `/`, `/@db`,
    `@script.sql`, `user@db`, options and `KEY=VALUE` words are
    no logon); a relative path after such a program in the same
    command (a script argument `in/data.csv`) is masked after
    its first `/`, an over-mask.

  They apply wherever the flag rules do: after a wrapper,
  inside shell strings. Reasons: each rule needs a program word
  before it, so it costs nothing elsewhere; a denied key keeps
  `config set color false` and `configure get region` readable.
  Rejected: a broad "a bare denied word masks the word after
  it" rule — it would mask whatever follows `cat password.txt`,
  and every argument after a file or key named like a secret,
  while still missing AUTH (not a denied word) and sqlplus (no
  key at all). Programs without a rule and a logon split at a
  quoted blank stay the `positional` accepted limit; a secret piped to stdin (`echo s
  | docker login --password-stdin`) is an argument of `echo`,
  marked only by the pipe, and stays the `stdin` limit
  ([schema/redaction.md](../../schema/redaction.md#accepted-limits)).

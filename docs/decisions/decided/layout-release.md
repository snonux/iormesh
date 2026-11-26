# Decided — repository layout, BPF C, release and Go

Part of [Decided](../decided.md), in [Decisions](../../decisions.md).

- **#30 Repository-root layout** — the root keeps only what
  the conventions pin. The Mage targets move into `magefiles/`
  (mage ≥ 1.17, the pinned version, reads the directory; all
  magefiles move together, keeping their `//go:build mage` tags
  and provenance headers, so plain `go vet ./...` and
  `go test ./...` keep skipping the mage `package main`). The
  build outputs go to `bin/` (gitignored; owner override, tasks
  rk4 and uk4, of the first cut that kept them at the root as
  in ior): the binaries `iormesh`, `iormeshctl`, `echopeer` and
  `edgepeer`, the four jars (`workload.jar`, `workload-otel.jar`,
  `workload-libs.jar`, `iormesh-agent.jar`) and the gates' test
  binaries (`integrationtests.test`, `iormesh-internal.test`).
  One directory for all of them keeps what had pinned the root
  working: the non-java-peer still finds `echopeer` beside the
  jar, the module jars' manifest `Class-Path` still names
  `workload.jar` beside them (the vendored jars as
  `../workload/…`), the integration harness's defaults become
  `../bin/iormesh`, `../bin/workload.jar` and `../bin/edgepeer`,
  and a scoped sudoers rule names
  `<repo>/bin/integrationtests.test`. `mage clean` removes
  `bin/` (and any root artifact of a tree built before the
  move); `dist/` stays the release output. The kind
  acceptance's state files
  (`.k8s-acceptance/`: the kubeconfig, the two `.state.json`s,
  `teardown.log`) group under one hidden directory.
  Rejected: the outputs at the root (the first cut, for ior's
  pattern) — they cluttered the top level; a `bin/java/` of its
  own for the jars — the jars and `echopeer` must share a
  directory, so a split buys nothing.

- **#31 Verifier-safe array indexes in the BPF C** — a variable
  index into a map value's array is bounded on the register
  that indexes, not only by a range check:
  `iormesh_index_mask(idx, n)` (`internal/c/maps.h`) hides
  idx's range from clang (`barrier_var`) and masks it with
  `n - 1` (n a power of two). A plain `if (idx >= n) return;
  a[idx]` leaves the compiler free to test a copy: Debian clang
  19.1.7 (the `tracer-arm64` builder) did, and both the el9
  5.14 and the 7.x verifiers refused `handle_sys_exit_read`
  (task il4). The source must load whatever reasonable clang
  builds it, so the fix is in the C, not a pinned compiler.
  Where the bound is not a power of two,
  `iormesh_index_clamp(idx, max)` does the same with an inline
  asm `if idx <= max goto +1; idx = max` on the register it
  returns. Applied to every clamp-then-index site:
  `rwagg.c`'s slot and `javamark.c`'s frame indexes (task il4),
  `filter.c`'s histogram bucket (mask, 8 buckets) and
  `fdname.c`'s name length (clamp, at most 67; task ll4). The
  builders still check their clang against a recorded version
  (`CLANG_VERSION` in
  `scripts/container/tracer-*.Containerfile`, held to a
  recorded value by `TestBuilderClangPinned`): not to make the
  C load, but so a compiler change cannot reach a release
  without the el9 verifier check first (docs/testing.md,
  "Builder clang pin"). Rejected: a newer clang in the builder
  only — it hides the pattern until the next compiler change;
  a mask without the barrier — clang drops a mask it proves
  redundant and is back to the checked copy; a power-of-two
  mask for the name length — 127 does not keep the index
  inside the 68-byte field, 63 would cut names of 64–67
  bytes.

- **#32 Unsigned release archives; operators sign** — owner
  decision (2026-11-26, asked during task 7k4; built by task
  5m4), amending #28 for releases. `mage release` builds and
  packs the per-arch archives with a `SHA256SUMS` only: no
  `.minisig`, no `minisign.pub` in an archive, no
  `SHA256SUMS.minisig`; it needs no `IORMESH_MINISIGN_KEY` and
  asks no password. Each artifact must still carry the release
  version's stamp (`release.CheckStamp`), so a stale build is
  never packed. Deploy and bootstrap keep verifying every build
  against the operator's pinned key exactly as #28 says, so the
  operator signs the builds they deploy with their own key:
  `iormeshctl sign -key SECRET-KEY [-version V] DIR` signs the
  release artifacts in an unpacked archive (the tracer, the
  agent jar, the controllers; nothing else there) with no
  repository checkout, and `mage sign` stays for a `dist/`
  built from source. Both run the same `internal/release` code
  (`SignRelease`/`SignDir`, `ReadSecretKey`): the stamp check
  before any file is signed, the trusted comment
  `file:<name> version:<x.y.z>`, a secret key that is a
  regular file, not a symlink, the caller's own, with no group
  or other permission, its password from
  `$IORMESH_MINISIGN_PASSWORD` or read without echo from the
  terminal, never printed. Why: the project holds no release
  key that every operator would have to trust; each operator
  already pins their own key on the controller and their
  hosts, so a project signature would add a second trust
  anchor nobody checks against, and a leaked project key would
  reach every operator who pinned it. `SHA256SUMS` catches a
  corrupt download; what is trusted is the operator's own
  signature after they checked it. Rejected: signed archives
  plus re-signing by the operator — two keys, and the project
  key protects nothing deploy checks; shipping the project's
  public key for operators to pin — the trust anchor this
  decision removes.
- **#33 Invalid UTF-8 in NDJSON, and the supported Go range** —
  the NDJSON writer (`schema.AppendEvent`) writes each invalid
  UTF-8 byte of a string as the six-byte escape `\ufffd`, by
  the project's own rule, not by whichever Go builds it. That
  is the bytes every trace and golden written so far holds:
  the encoder copied Go 1.26's `json.Marshal`, and Go 1.27's
  `encoding/json` now writes the raw U+FFFD bytes instead (task
  8m4). Both decode to the same string, so readers on either
  Go see the same event; only the bytes would differ, and
  byte-stable output for the same event (docs/schema.md,
  "NDJSON framing") is the contract. The encoder's test takes
  `json.Marshal` as its reference for everything else and the
  fixed `\ufffd` escape for invalid UTF-8. Go: go.mod's
  `go 1.26.0` is the floor, CI builds with it, and Go 1.27 is
  supported too (the local gates pass on both); go.mod gets no
  `toolchain` line, which would force a toolchain download on
  every host with another Go. Source must be gofmt-clean under
  both versions: where their gofmt differ (1.27 aligns a map
  literal's keys differently when one key is far longer), the
  code takes a form both print alike. Rejected: following the
  installed Go's `json.Marshal` — a Go update would silently
  change every trace's bytes; a `toolchain go1.26.x` line —
  it hides the 1.27 difference instead of settling it and
  costs a download.
- **#38 `iormeshctl keygen` makes the operator's key without a
  terminal** — task vm4 (2026-11-26), completing #32 for #37's
  agent: `minisign -G` asks for the password on a terminal, so
  an agent or a script holding it in
  `$IORMESH_MINISIGN_PASSWORD` could sign but not make the key.
  `iormeshctl keygen -key SECRET-KEY [-pub PUBLIC-KEY]` makes the
  pair with the library `sign` already uses (`aead.dev/minisign`,
  `release.WriteKeyPair`): the secret key encrypted with the
  password exactly as `minisign -G` does (scrypt, the same
  file format, so either tool reads the other's keys), the
  public key at `-pub`, by default the pinned key path verify,
  deploy and bootstrap read (`~/.config/iormesh/minisign.pub`).
  Both files are new and 0600: an existing file or symlink at
  either path is refused before anything is written, each is
  created `O_CREAT|O_EXCL|O_NOFOLLOW` (the path `sign`'s
  signature writes use), and a failed public key write removes
  the secret key just made. A missing directory is made 0700,
  and each key's directory must be the caller's and writable by
  nobody else, as #28 holds the pinned key's. Nothing it prints
  holds the password or the secret key: the two paths and the
  key id. The password comes only from the environment, and an
  unset or empty variable is refused; there is no `-unencrypted`
  flag. Why: `ReadSecretKey`, and so `sign` and `mage sign`,
  read only a password-encrypted key, so an unencrypted one
  could sign nothing here and would protect every host that
  pins it with a file read alone; a terminal prompt would
  duplicate `minisign -G`, which asks twice and stays the way
  by hand. Choosing the password stays a **Human step**; an
  agent may run `keygen` once the human put the password in the
  environment, as it may run `sign`. Rejected: documenting a
  piped `minisign -G` — the Go CLI
  (`aead.dev/minisign/cmd/minisign@v0.3.0`) reads one line of
  stdin as the password when stdin is no terminal, but chapter 2
  lets the operator install either CLI (a distribution's is
  jedisct1's C one, whose non-terminal behaviour is its own), the
  pipe is one more thing a smaller agent gets wrong (a trailing
  newline, an `echo` that puts the password in the process
  list), and the release archive already holds the controller,
  so `keygen` needs no extra package; an unencrypted key for
  agents — see above.
- **#40 An untagged build for tests, marked and kept apart** —
  task 7n4 (2026-11-27), for #37's trial: `mage release` cuts
  only a clean HEAD at the tag `v<internal/version.Version>`,
  and the commit under test has no tag, so the first trial
  tagged it locally for the build and deleted the tag.
  `mage releaseUntagged ENGINE` runs Release's builds, stamp
  checks and packing (`internal/dist`) on a clean HEAD that is
  not that tag, into `dist/untagged/<version>/`, never
  `dist/release/`. Next to `SHA256SUMS` and inside every
  archive, beside the artifacts, it writes `UNTAGGED`, whose
  first line reads `iormesh <version> untagged build of commit
  <full sha>: NOT a release`, then the `git describe` and why.
  A dirty tree is refused, so the build is one commit; a HEAD at
  the release tag is refused, so the note is never false.
  `mage release` is unchanged and still refuses every untagged
  commit. The trial serves `dist/untagged/<version>/` by
  default (`IORMESH_TRIAL_RELEASE_DIR` names another, such as a
  tagged release's). Why: the artifacts' version is the
  compiled-in stamp, and `internal/version` allows
  MAJOR.MINOR.PATCH only, because signing's trusted comment,
  deploy's verification and the upgrade's downgrade check (#1)
  compare it; so the build keeps the stamp and the archive names
  of its source's version, which also keeps the trial on guide
  chapter 3's path, `iormesh-<version>-<arch>.tar.gz` and all.
  What marks it is where it lands and the note it carries, not a
  version string. Rejected: a pre-release or build-metadata
  version (`0.1.0-trial.<sha>`) — every version check would
  have to learn a second spelling, and deploy and the upgrade
  would order builds by it; a documented temporary tag — a
  `v*` tag on an unreleased commit is one push away from a
  false release; an explicit `VERSION` override for `mage
  release` — the stamp is compiled from the source, so the
  override would name a version the binaries do not carry.

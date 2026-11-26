# Signing and releases

Part of [Build & code generation](../build.md).

## Signing

Hosts run only builds signed with minisign (decisions.md #28,
built with its default by task bb4) — signed by the operator,
with their own key: release archives ship unsigned
(decisions.md #32). There are two ways to sign, one
implementation (`internal/release`):

```bash
# an unpacked release archive ("Releases" below), no checkout:
iormeshctl sign -key ~/.minisign/iormesh.key iormesh-0.1.0-amd64
# or a dist/ built from source, after the container builds:
export IORMESH_MINISIGN_KEY=~/.minisign/iormesh.key  # outside the tree
mage agentBuild    # also dist/iormesh-agent.jar (task kf4)
mage sign          # dist/NAME -> dist/NAME.minisig, then verified
iormeshctl verify dist/iormesh-linux-amd64   # the controller's check
```

- **Key**: a minisign key pair made once, e.g. `minisign -G -p
  iormesh.pub -s iormesh.key`, with a password (the Go library
  reads only scrypt-encrypted secret keys). It needs the
  `minisign` CLI (your distribution's or Homebrew's `minisign`
  package): iormeshctl signs and verifies but makes no keys.
  The secret key is the operator's and never enters the
  repository: `mage sign` refuses a key path inside the tree. Both signers refuse a key
  file that is a symlink, not a regular file, someone else's,
  or open to group or other (`chmod 600` it).
  The password comes from the terminal prompt (no echo), or from
  `$IORMESH_MINISIGN_PASSWORD` for unattended runs; it is never
  printed. Prefer the
  prompt or a CI secret store that masks it: an environment
  variable is readable by every process of the same uid
  (`/proc/<pid>/environ`) and leaks into logs that dump the
  environment.
- **What is signed**: `iormeshctl sign DIR` signs the release
  artifacts in DIR — `iormesh-linux-<arch>`,
  `iormesh-agent.jar`, `iormeshctl-{linux,darwin}-<arch>` —
  and nothing else there (`LICENSE`, `CHANGELOG.md`), at
  `-version` (default its own version). `mage sign` signs each
  file in `dist/` (not hidden, not a
  `.minisig`) — the tracers, the controllers and, after `mage
  agentBuild`, the agent jar `iormesh-agent.jar`, which
  `iormeshctl deploy` ships to hosts whose projects use the
  agent and `iormesh upgrade` installs only at the tracer's
  version (task kf4; [security.md](../security.md)) — prehashed
  (BLAKE2b-512, minisign's default),
  with the trusted comment
  `timestamp:<unix> file:<name> version:<x.y.z>`; the version
  is `internal/version.Version`, bumped per release. The tracer
  and the controller both check the signed `file:` and
  `version:`, so the comment is part of what is verified.
- **Stale builds are refused** (task uf4): every artifact
  carries its own version, and `mage sign` (or `iormeshctl
  sign`, against `-version`) checks all of them
  against `internal/version.Version` before it signs any. The
  jar's manifest has `Implementation-Version` (written by `mage
  agentBuild`); the tracers and controllers carry
  `version.Stamp`, `"\0iormesh-version:<x.y.z>\0"`, in their
  rodata (kept linked by the `version` subcommands), read
  statically, since the signing host cannot run an arm64 or
  darwin build to ask its `--version`. A missing or different
  stamp fails the run with `rebuild it` and leaves `dist/`
  unsigned, so a jar or tracer left from an older build is never
  signed as the new release. `TestControllerCrossCompiles` and
  `TestTracerCarriesVersionStamp` (`internal/buildgate`) keep
  the stamp in real builds. The stamp proves the version only,
  not the commit (task 8g4): a build of another commit at the
  same version would pass. No vcs revision is stamped — the
  container builds get no `.git` (`.containerignore`), so Go's
  `-buildvcs` has nothing to embed, and the jar would need its
  own — so the release rule is to bump the version, commit, and
  rebuild `dist/` from that commit right before `mage sign`.
- **Rebuilding without a version bump**: through the sudoers
  rule `upgrade` refuses a jar (as a tracer) with other bytes
  under an installed version, so a rebuilt and re-signed jar at
  the same version makes `iormeshctl deploy` fail on every host
  that already has the jar — closed, nothing is started there.
  Bump `internal/version.Version` for a release, or reinstall
  as root for a test host (docs/testing.md, the M4 runbook).
- **Library**: `aead.dev/minisign`, pinned by go.mod/go.sum
  (and `golang.org/x/crypto` for BLAKE2b and scrypt). The files
  are ordinary minisign: `minisign -Vm FILE -p iormesh.pub`
  checks them, and the `minisign` tool can sign instead of
  `mage sign` with `-t "file:NAME version:X.Y.Z"`.
- **Pinned public key**: `~/.config/iormesh/minisign.pub` on the
  controller (`iormeshctl verify`, `-key` overrides; `iormeshctl
  deploy` checks every build with it before pushing),
  `/opt/iormesh/etc/minisign.pub` (root-owned) on each server,
  where `iormesh upgrade FILE` verifies `FILE.minisig` before it
  replaces `/opt/iormesh/bin/iormesh` ([security.md](../security.md)
  has the checks).


## Releases

`mage release` turns a tagged commit into downloadable archives
(task rh4, owner-approved 2026-11-22). The archives are unsigned
(decisions.md #32, task 5m4): no key, no password, no
`.minisig`. It publishes nothing: uploading the files is a human
step.

```bash
# bump internal/version.Version, commit, then:
git tag v0.1.0                # exactly v<internal/version.Version>
mage release podman           # or docker; no IORMESH_MINISIGN_KEY
ls dist/release/0.1.0/
# iormesh-0.1.0-amd64.tar.gz  iormesh-0.1.0-arm64.tar.gz  SHA256SUMS
```

- **Refused up front**: a dirty tree, a HEAD past the tag, or a
  tag other than `v<internal/version.Version>` (`git describe
  --tags --dirty --match 'v[0-9]*'` must print exactly the tag).
- **Steps**: every container build (`containerBuild ENGINE
  all`), `agentBuild`, then `internal/dist` packs a fresh
  `dist/release/<version>/` and writes its `SHA256SUMS`.
- **Archive contents**, under `iormesh-<version>-<arch>/`:
  `iormesh-linux-<arch>`, `iormesh-agent.jar`,
  `iormeshctl-linux-<arch>`, `iormeshctl-darwin-<arch>`;
  `CHANGELOG.md`; `LICENSE` (MIT). Each of the last two is
  packed when present, and a missing one is noted. Each artifact
  must carry the release version's stamp (as `mage sign`
  checks, "Stale builds are refused" above), checked on the
  bytes packed; a `.minisig` left in `dist/` is not packed.
- **Reproducible**: entries sorted, owner root 0/0, modes
  0755 (binaries, the top directory) or 0644, every mtime
  `$SOURCE_DATE_EPOCH` or else the commit time, gzip header
  without name or time. The same `dist/` packs to the same
  bytes with the same Go toolchain (its gzip encoder could change
  between releases; `internal/dist`'s `TestPackDeterministic`).
  The container builds rebuild to the same bytes too (two runs
  of the v0.1.0 dry run, [testing.md](../testing.md), task 7k4),
  and so does the agent jar (task bm4): `mage agentBuild` packs
  it and its nested bridge jar with `jar --create --date`, every
  entry dated `$SOURCE_DATE_EPOCH` or else the commit time (so
  outside a git checkout every jar target needs
  `SOURCE_DATE_EPOCH` set), and the jar tool sorts the entries
  by name. What still varies is
  the JDK: javac's class files and the manifest's `Created-By`
  (the jar tool's JDK version, e.g. `25.0.5 (Red Hat, Inc.)`)
  come from the builder's JDK, which the repository does not
  pin: `mage release` builds the agent with the `java` on the
  release machine's `PATH`, not in a container. A rebuild
  matches only with the same JDK build, version and vendor, as
  the manifest's `Created-By` names — read it from the
  published jar's manifest (`unzip -p
  iormesh-agent.jar META-INF/MANIFEST.MF`). `mage
  agentReproTest` (in `mage agentTest` and CI) builds the jar
  from scratch twice and fails unless the SHA-256s match. A
  kept jar whose entries carry another date (an older commit)
  is rebuilt, so a fresh jar is never one a clean build of this
  commit would not reproduce; freshness otherwise still goes by
  file mtimes, which `--date` leaves at the build time. The
  workload jars are dated the same way, though not released.

An operator goes from a download (or their own `mage release`)
to a deploy in four steps: check, unpack, sign with their own
key, deploy. `SHA256SUMS` catches a corrupt or truncated
download; it is not a signature, so trusting the archive is the
operator's call (where it came from, or a rebuild of the tag
compared byte for byte, with the JDK release the published jar
names, "Reproducible" above). The key
comes from the `minisign` CLI (`minisign -G`, "Key" under
[Signing](#signing)); iormeshctl has no key generator. What
deploy and bootstrap trust from then on is the operator's own
signature:

```bash
sha256sum -c --ignore-missing SHA256SUMS   # the archives you have
tar -xzf iormesh-0.1.0-amd64.tar.gz
cd iormesh-0.1.0-amd64
./iormeshctl-linux-amd64 sign -key ~/.minisign/iormesh.key .
./iormeshctl-linux-amd64 verify iormesh-linux-amd64 \
  iormesh-agent.jar iormeshctl-linux-amd64  # every build: OK
./iormeshctl-linux-amd64 deploy -dist . HOST
```

`sign` asks for the key's password on the terminal (or reads
`$IORMESH_MINISIGN_PASSWORD`), signs the four artifacts at its
own version (`-version` for another; each artifact's stamp must
match), and verifies every signature it wrote. `verify` and
`deploy` use the pinned `~/.config/iormesh/minisign.pub` — the
public half of that same key ([Signing](#signing)). Hosts of
both arches: sign both unpacked archives and copy the arm64
tracer with its `.minisig` next to the amd64 files, so one
`-dist` holds both tracers.

**Licence** (owner, 2026-11-22): MIT for the whole repository
(`LICENSE`). The BPF object keeps declaring `Dual BSD/GPL`: the
kernel reads that string to allow GPL-only helpers, and MIT code
is compatible with both. Third-party parts keep their own
licences: ObjectWeb ASM in the agent
(`agent/third_party/asm/LICENSE.txt`), libbpf linked into the
tracer (LGPL-2.1 OR BSD-2-Clause), libbpfgo (Apache-2.0).

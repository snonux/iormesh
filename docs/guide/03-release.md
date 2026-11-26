# 3. Get and sign a release

Hosts run only builds signed with your key. The laptop checks the
signature before a file leaves it, and the host's tracer checks
it again, against its own root-owned copy of the public key,
before it installs anything. Release archives ship unsigned
(checksums only; [decisions.md #32](../decisions.md)): you sign
the builds you deploy yourself. This chapter makes the key, gets
a release — downloaded or built — checks it, signs it and
installs iormeshctl.

## Make your key, once

> **Human step.** The secret key's password protects every host
> that trusts it. Choose it yourself, type it yourself, and keep
> the secret key outside any repository (`mage sign` refuses a
> key path inside the tree).

```sh
mkdir -p ~/.config/iormesh ~/.minisign
minisign -G -p ~/.config/iormesh/minisign.pub -s ~/.minisign/iormesh.key
chmod 600 ~/.minisign/iormesh.key
```

This needs the `minisign` CLI from chapter 2; iormeshctl signs
and verifies but makes no keys. Give the key a password (`-G`
asks for it): the signing library reads only
password-encrypted secret keys, and both signers refuse a key
file others may read. `~/.config/iormesh/minisign.pub` is where
iormeshctl looks for the pinned public key by default (`-key`
overrides it on `verify`, `bootstrap`, `deploy` and `expand
-apply`).

**Checkpoint:** both files exist; `minisign.pub` starts with
`untrusted comment: minisign public key`.

## Get a release

Either download the archive for your hosts' arch and
`SHA256SUMS`, or build them from the release tag with `mage
release podman` (they land in `dist/release/<version>/`;
[build.md](../build.md#releases)). Then check and unpack:

```sh
sha256sum -c --ignore-missing SHA256SUMS   # the archives you have
tar -xzf iormesh-0.1.0-amd64.tar.gz
```

`SHA256SUMS` catches a corrupt download; it is no signature.
Whether to trust the archive is your call — where it came from,
or your own `mage release` of the tag compared with it byte for
byte (build with the JDK release that the published jar's
`Created-By` names, [build.md](../build.md#releases)) — and your
signature, next, is what deploy trusts from then on.

**Checkpoint:** `sha256sum -c` prints `OK` for the archive;
`iormesh-0.1.0-amd64/` holds `iormesh-linux-amd64`,
`iormesh-agent.jar`, `iormeshctl-{linux,darwin}-amd64`,
`CHANGELOG.md` and `LICENSE`.

## Sign it with your key

> **Human step.** `sign` asks for the key's password on the
> terminal, without echo. For unattended runs it reads
> `$IORMESH_MINISIGN_PASSWORD` instead; it never prints it.

```sh
cd iormesh-0.1.0-amd64
./iormeshctl-linux-amd64 sign -key ~/.minisign/iormesh.key .
```

Run for this guide in an unpacked release (password from the
environment; then a `-version` the builds do not carry):

```text
$ ./iormeshctl-linux-amd64 sign -key ../keys/iormesh.key .
iormesh-agent.jar: signed iormesh-agent.jar 0.1.0
iormesh-linux-amd64: signed iormesh-linux-amd64 0.1.0
iormeshctl-darwin-amd64: signed iormeshctl-darwin-amd64 0.1.0
iormeshctl-linux-amd64: signed iormeshctl-linux-amd64 0.1.0
4 files signed by key EE874382FAAF64B0
$ ./iormeshctl-linux-amd64 sign -key ../keys/iormesh.key -version 0.2.0 .
iormeshctl: sign: artifact is not stamped with the version being signed:
iormesh-agent.jar is stamped 0.1.0, signing 0.2.0; rebuild it
[exit status 1]
```

(The refusal is one line; it is wrapped here.)

It signs the release builds only, each at the controller's own
version (`-version` for another), and refuses before signing
anything when a build carries another version. A key file
others may read is refused with `chmod 600`; a wrong password
with `decryption failed`.

Building from source instead of a release? `mage buildPodman`
(or `buildDocker`) and `mage agentBuild` fill `dist/`, and
`IORMESH_MINISIGN_KEY=~/.minisign/iormesh.key mage sign` signs
it the same way ([build.md](../build.md#signing)); `mage build`
and the other development targets write to `bin/` instead,
which hosts never take.

**Checkpoint:** every build in the directory has a `.minisig`
next to it; `LICENSE` has none.

## Install iormeshctl and verify

Install the controller for your laptop (on an Apple silicon Mac,
`iormeshctl-darwin-arm64` from the arm64 archive), then check
builds with it, still in the release directory:

```sh
install -m 0755 iormeshctl-linux-amd64 ~/bin/iormeshctl
iormeshctl verify iormesh-linux-amd64 iormesh-agent.jar
```

Run for this guide, on a laptop's `dist/` (two good files, then a
stale one):

```text
$ iormeshctl verify dist/iormeshctl-linux-amd64 dist/iormesh-linux-arm64
dist/iormeshctl-linux-amd64: OK, signed iormeshctl-linux-amd64 0.1.0
dist/iormesh-linux-arm64: OK, signed iormesh-linux-arm64 0.1.0
$ iormeshctl verify dist/iormesh-linux-amd64
iormeshctl: dist/iormesh-linux-amd64: FAILED: dist/iormesh-linux-amd64: signature verification failed: the file does not match its signature
iormeshctl: 1 of 1 files failed verification
[exit status 1]
```

Success is one `OK, signed <file> <version>` line per file.
`the file does not match its signature` means the file changed
after signing: sign again. Other refusals (another key, a
renamed file) are in
[troubleshooting.md](../troubleshooting.md#signatures-and-pinned-keys).

`bootstrap`, `deploy` and `expand -apply` read `./dist` unless
`-dist` names another directory: pass `-dist` with the signed
release directory, or run them from a repository root whose
`dist/` you signed. For hosts of both arches, sign both
archives and copy the arm64 tracer and its `.minisig` next to
the amd64 files, so one directory holds both.

Reference: [build.md](../build.md#signing) (the key, what is
signed, stale builds), [build.md](../build.md#releases) (release
archives), [security.md](../security.md) (the checks on each
side).

---

← [2. Prepare the laptop and the hosts](02-prerequisites.md) · [Contents](README.md) · Next: [4. Register hosts and bootstrap them](04-register-bootstrap.md) →

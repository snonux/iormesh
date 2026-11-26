# 3. Get and sign a release

Hosts run only builds signed with your key. The laptop checks the
signature before a file leaves it, and the host's tracer checks
it again, against its own root-owned copy of the public key,
before it installs anything. Release archives ship unsigned
(checksums only; [decisions.md #32](../decisions.md)): you sign
the builds you deploy yourself. This chapter gets a release —
downloaded or built — checks it, makes your key, signs the
release and installs iormeshctl.

## Get a release

Either download the archive for your hosts' arch and
`SHA256SUMS`, or build them from the release tag with `mage
release podman` (they land in `dist/release/<version>/`;
[build.md](../build/signing-releases.md#releases)). Then check and unpack:

```sh
sha256sum -c --ignore-missing SHA256SUMS   # the archives you have
tar -xzf iormesh-0.1.0-amd64.tar.gz
```

`SHA256SUMS` catches a corrupt download; it is no signature.
Whether to trust the archive is your call — where it came from,
or your own `mage release` of the tag compared with it byte for
byte (build with the JDK build, version and vendor, that the
published jar's `Created-By` names, [build.md](../build/signing-releases.md#releases)) — and your
signature, next, is what deploy trusts from then on.

**Checkpoint:** `sha256sum -c` prints `OK` for the archive;
`iormesh-0.1.0-amd64/` holds `iormesh-linux-amd64`,
`iormesh-agent.jar`, `iormeshctl-{linux,darwin}-amd64`,
`CHANGELOG.md` and `LICENSE`.

## Make your key, once

> **Human step.** The secret key's password protects every host
> that trusts it. Choose it yourself and keep the secret key
> outside any repository (`mage sign` refuses a key path inside
> the tree). For `keygen` and `sign`, put it in
> `$IORMESH_MINISIGN_PASSWORD` yourself — `read -rs
> IORMESH_MINISIGN_PASSWORD; export IORMESH_MINISIGN_PASSWORD`,
> not on a command line the shell history keeps; an agent may
> run both once it is there, and never prints it.

With the controller you just unpacked, no terminal needed:

```sh
cd iormesh-0.1.0-amd64
./iormeshctl-linux-amd64 keygen -key ~/.minisign/iormesh.key
```

It writes the password-encrypted secret key at `-key` and its
public half at `~/.config/iormesh/minisign.pub`, where iormeshctl
looks for the pinned public key by default (`-pub` writes it
elsewhere; `-key` overrides the default on `verify`,
`bootstrap`, `deploy` and `expand -apply`). Both are new 0600
files and missing directories are made 0700; it refuses an
existing file rather than replace a key hosts may already pin,
an unset or empty `$IORMESH_MINISIGN_PASSWORD`, and prints only
the paths and the key id ([decisions.md #38](../decisions.md)).

Run for this guide in a scratch directory (a throwaway key,
then once more over it):

```text
$ ./iormeshctl keygen -key keys/iormesh.key -pub keys/minisign.pub
secret key: keys/iormesh.key
public key: keys/minisign.pub
key id: F5D0CA9166F0AD42
$ ./iormeshctl keygen -key keys/iormesh.key -pub keys/minisign.pub
iormeshctl: keygen: keys/iormesh.key: file already exists; keygen never replaces a key
[exit status 1]
```

By hand, the `minisign` CLI from chapter 2 makes the same pair
and asks for the password twice on the terminal:

```sh
mkdir -p -m 0700 ~/.config/iormesh ~/.minisign
minisign -G -p ~/.config/iormesh/minisign.pub -s ~/.minisign/iormesh.key
chmod 600 ~/.minisign/iormesh.key
```

`-m 0700` keeps both directories yours alone; a plain `mkdir -p`
makes them 0755, and iormeshctl later writes the registry into
that directory as it finds it. Give the key a password: the
signing library reads only password-encrypted secret keys, and
both signers refuse a key file others may read.

**Checkpoint:** both files exist; `minisign.pub` starts with
`untrusted comment: minisign public key`.

## Sign it with your key

> **Human step.** `sign` asks for the key's password on the
> terminal, without echo. For unattended runs it reads
> `$IORMESH_MINISIGN_PASSWORD` instead; it never prints it.

Still in the release directory:

```sh
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
it the same way ([build.md](../build/signing-releases.md#signing)); so does
`dist/iormeshctl-linux-amd64 sign -key ~/.minisign/iormesh.key
dist`. For amd64 hosts only, `mage containerBuild podman
tracer-amd64,ctl` is the lighter build (the amd64 tracer and the
four controllers). `mage build` and the other development
targets write to `bin/` instead, which hosts never take.

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

Reference: [build.md](../build/signing-releases.md#signing) (the key, what is
signed, stale builds), [build.md](../build/signing-releases.md#releases) (release
archives), [security.md](../security.md) (the checks on each
side).

---

← [2. Prepare the laptop and the hosts](02-prerequisites.md) · [Contents](README.md) · Next: [4. Register hosts and bootstrap them](04-register-bootstrap.md) →

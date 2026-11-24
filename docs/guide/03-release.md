# 3. Build and sign a release

Hosts run only builds signed with your key. The laptop checks the
signature before a file leaves it, and the host's tracer checks
it again, against its own root-owned copy of the public key,
before it installs anything. This chapter makes the key, builds
`dist/`, signs it and installs iormeshctl.

## Make the release key, once

> **Human step.** The secret key's password protects every host
> that trusts it. Choose it yourself, type it yourself, and keep
> the secret key outside the repository (`mage sign` refuses a
> key path inside the tree).

```sh
mkdir -p ~/.config/iormesh ~/.minisign
minisign -G -p ~/.config/iormesh/minisign.pub -s ~/.minisign/iormesh.key
```

Give the key a password: the signing library reads only
password-encrypted secret keys. `~/.config/iormesh/minisign.pub`
is where iormeshctl looks for the pinned public key by default
(`-key` overrides it on `verify`, `bootstrap`, `deploy` and
`expand -apply`).

**Checkpoint:** both files exist; `minisign.pub` starts with
`untrusted comment: minisign public key`.

## Build and sign `dist/`

From the repository root:

```sh
mage buildPodman      # or: mage buildDocker; tracers and controllers in dist/
mage agentBuild       # the Java agent; needed when a project uses it (chapter 8)
IORMESH_MINISIGN_KEY=~/.minisign/iormesh.key mage sign
```

`mage sign` asks for the key's password on the terminal (a human
step) and writes `NAME.minisig` next to every file in `dist/`. It
refuses to sign a file built from another version than
`internal/version.Version` (`… rebuild it`): rebuild `dist/` from
the current commit, then sign.

`mage build` and the other development targets write to `bin/`
instead. Hosts take only the signed release files in `dist/`.

**Checkpoint:** `ls dist/` lists `iormesh-linux-amd64`,
`iormesh-linux-arm64`, `iormeshctl-{darwin,linux}-{amd64,arm64}`
and `iormesh-agent.jar`, each with a `.minisig`.

## Install iormeshctl and verify

Install the controller for your laptop, then check builds with
it:

```sh
install -m 0755 dist/iormeshctl-linux-amd64 ~/bin/iormeshctl   # or …-darwin-arm64
iormeshctl verify dist/iormesh-linux-amd64 dist/iormesh-agent.jar
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
`the file does not match its signature` means the file was
rebuilt after signing: run `mage sign` again. Other refusals
(another key, a renamed file) are in
[troubleshooting.md](../troubleshooting.md#signatures-and-pinned-keys).

Run the later commands from the repository root: `bootstrap`,
`deploy` and `expand -apply` read `./dist` (`-dist` names another
directory).

Reference: [build.md](../build.md#signing) (the key, what is
signed, stale builds), [build.md](../build.md#releases) (release
archives), [security.md](../security.md) (the checks on each
side).

---

← [2. Prepare the laptop and the hosts](02-prerequisites.md) · [Contents](README.md) · Next: [4. Register hosts and bootstrap them](04-register-bootstrap.md) →

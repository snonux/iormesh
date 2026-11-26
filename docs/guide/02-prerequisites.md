# 2. Prepare the laptop and the hosts

Before anything is installed, check that the laptop can reach
each host the way iormeshctl will, and that each host can run the
tracer. Every item has a check you can run; do them all once.

## The laptop (the controller)

Linux or macOS; it needs no eBPF and no root.

| Needs | Check | Success looks like |
|---|---|---|
| only to build a release yourself: this repository, Go, [mage](https://magefile.org) ≥ 1.17 | `mage -version` | a version of 1.17 or newer |
| only to build a release yourself: Podman or Docker | `podman version` (or `docker version`) | client and server versions, no error |
| optional: `minisign` (your distribution's or Homebrew's package; no package? `go install aead.dev/minisign/cmd/minisign@v0.3.0`, the library iormeshctl signs with), to make your signing key by hand — `iormeshctl keygen` makes it too ([ch. 3](03-release.md#make-your-key-once)) | `minisign -v` | a version line |
| rsync 3.4.0 or newer, or a patched distribution build, first in `PATH` | `rsync --version` | `rsync  version 3.4.0` or newer; 3.2.4 to 3.3.x: the check below |
| `tar`, to unpack the release (a minimal Rocky 9 lacks it) | `command -v tar` | a path; else `sudo dnf install -y tar` |
| key-based SSH to each host through your agent | `ssh -o BatchMode=yes app1 true` | returns at once, no prompt, exit 0 |
| each host's key in `~/.ssh/known_hosts` | the same command | no `Host key verification failed.` |

Why so strict:

- iormeshctl runs `ssh app1` with the host exactly as the
  registry names it, so the SSH user comes from `~/.ssh/config`
  (`Host app1` / `User ops`) or from registering the host as
  `ops@app1`. Check with the same name you will register.
- iormeshctl runs `ssh` and `rsync` with `BatchMode=yes` and
  `StrictHostKeyChecking=yes`. It never prompts, and it never
  trusts a host key on first use. A key that needs a passphrase
  must be in your agent (`ssh-add`).
- An older rsync on the receiving side lets a malicious server
  write outside the store. macOS ships rsync 2.6.9 or openrsync:
  install a current rsync (Homebrew `rsync`) and put it first in
  `PATH`.
- A distribution's rsync older than 3.4.0 is fine when it
  carries the two fixes backported (decisions.md #39) — Rocky
  9.9's `rsync-3.2.7-1.el9_8` does. One line per distribution;
  success shows both fixes (on el9, two `Resolves:` lines):

  ```sh
  # Rocky, RHEL, Alma 9 (the changelog names Red Hat's ids):
  rpm -q --changelog -f "$(command -v rsync)" | grep -E 'RHEL-70208|RHEL-70210'
  # Debian, Ubuntu (rsync in /usr/bin):
  zgrep -E 'CVE-2024-1208[78]' /usr/share/doc/rsync/changelog.Debian.gz
  ```

  Fedora and Homebrew ship 3.4 or newer. A fix missing:
  update the distribution's package (`sudo dnf update -y
  rsync`) and check again; never build rsync from an unnamed
  source.

> **Human step.** Accepting a host key is a trust decision: log
> in once by hand (`ssh app1`), compare the fingerprint with
> one you got out of band (the host's console, your inventory),
> and only then accept it. An agent must not accept host keys for
> you.

Reference: [security.md](../security.md) (the SSH and rsync rules),
[build.md](../build/containers.md#container-builds) (the build engines).

## Each host

Rocky Linux 9.9 on x86_64 is the main target. For arm64: the
verifier refusal on el9 aarch64 is fixed in the source (decision
#31), but a rebuilt release has not been verified there yet
([build.md](../build.md), "arm64 status").

Run these on the host as `ops`:

| Needs | Check | Success looks like |
|---|---|---|
| the stock el9 kernel with BTF | `uname -r; ls /sys/kernel/btf/vmlinux` | `5.14.0-…el9_9…` and the file exists |
| systemd | `command -v systemd-run` | a path |
| rsync (a minimal install lacks it) | `command -v rsync` | a path; else `sudo dnf install -y rsync` |
| a POSIX login shell for `ops` | `getent passwd ops` | ends in `/bin/bash` or another POSIX shell |
| `/opt` root-owned, not writable by others | `stat -c '%U %a' /opt` | `root 755` |
| sudo for `ops`, once | `sudo -l` | lists `(ALL) ALL` or a rule that covers the install (a password prompt is fine) |

No firewall change is needed: the tracer listens on nothing, and
everything goes through SSH.

> **Human step.** `ops` needs sudo once, for the first install
> ([chapter 4](04-register-bootstrap.md)). Granting it, and typing
> the password, is the host owner's call.

## Before you trace anything

iormesh traces production processes: it records their file
paths, addresses, HTTP methods and paths (secrets in headers,
query strings and command lines are redacted), and the Java
stacks behind them. Agree with the owner of each host what may
be traced and for how long. The collected traces are production
metadata on your laptop; treat them that way.

Reference: [trace-scope.md](../trace-scope.md) (what is traced),
[security.md](../security.md) (redaction, the store's
permissions).

---

← [1. How iormesh works](01-how-iormesh-works.md) · [Contents](README.md) · Next: [3. Get and sign a release](03-release.md) →

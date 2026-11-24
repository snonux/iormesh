# 4. Register hosts and bootstrap them

`add` writes a host and a project into the registry; it contacts
nothing. `bootstrap` installs the tracer on a registered host,
once, with your own sudo. After this chapter each host has
`/opt/iormesh/bin/iormesh`, its pinned key and one sudoers rule.

## Register the hosts

Flags of iormeshctl go **before** HOST; flags after HOST are the
tracer's and are stored with the entry:

```sh
iormeshctl add -project shop -pattern 'java -jar */shop.jar*' app1 -duration 8h
iormeshctl add -project billing -listen-port 8443 app2 -duration 8h
iormeshctl list
```

Run for this guide (scratch registry):

```text
$ iormeshctl add -project shop -pattern 'java -jar */shop.jar*' app1 -duration 8h
added app1 project shop (~/.config/iormesh/servers.yaml)
$ iormeshctl add -project billing -listen-port 8443 app2 -duration 8h
added app2 project billing (~/.config/iormesh/servers.yaml)
$ iormeshctl list
KIND  TARGET  PROJECT  TRACES                         FLAGS         NOTE
host  app1    shop     pattern java -jar */shop.jar*  -duration 8h  -
host  app2    billing  listen-port 8443               -duration 8h  -
```

The registry is plain YAML, written 0600 in a 0700 directory:

```yaml
servers:
  - host: app1
    projects:
      - name: shop
        target:
          pattern: java -jar */shop.jar*
        flags:
          - -duration
          - 8h
  - host: app2
    projects:
      - name: billing
        target:
          listen_port: 8443
        flags:
          - -duration
          - 8h
```

### Choosing the target

- **`-pattern GLOB`** is matched against the whole command line
  (as `ps -eo args` shows it) or the executable path, at every
  exec and against the processes already running when the
  session starts. It usually needs `*` at both ends. It never
  matches the 15-character process name.
- **`-listen-port N`** traces whoever owns that listening socket
  when the session starts, so the server must be up before
  `deploy`. When the server restarts, the session follows the
  port to the new owner if it is the same program under the same
  uid, within `-listen-grace` (2m by default; put e.g.
  `-listen-grace 5m` after HOST for a slow restart).
- **`-duration`** bounds every session (4h by default, 7 days at
  most). [Chapter 9](09-tracing-for-days.md) keeps sessions
  going past it.
- For Java stacks, add `-spool` and `-agent` after HOST now
  ([chapter 8](08-java-query.md)); a project is registered once
  per host, and `add` refuses it again
  (`registry: already registered: project "shop" on app1`).

**If not:** a flag in the wrong place is refused with the usage
text (exit 2), for example
`iormeshctl: usage: -pattern after HOST: iormeshctl flags go before HOST, and the registry entry sets the tracer's`;
a project without a target with
`target spec: give exactly one of PIDs, a pattern or a listening port (0 given)`.
A tracer flag a deployed session may not use (`-out`,
`-cpuprofile`, `-ready`, a `-trace-dir` off its path) is refused
here too.

Reference: [trace-scope.md](../trace-scope.md) (what a target
traces), decision #23 in [decisions.md](../decisions.md) (following
a port across restarts).

## Bootstrap: the one-time install

`deploy` only upgrades a tracer that is already installed, through
a narrow sudoers rule. The first install is your own sudo, once
per host. `bootstrap` verifies `dist/iormesh-linux-<arch>` against
the pinned key before anything leaves the laptop, then prints the
exact steps (default) or runs them (`-apply`).

### Printed steps (sudo with a password)

```sh
iormeshctl bootstrap -user ops app1     # prints the steps, contacts nothing
```

Run for this guide (for an arm64 host, `-arch arm64`, because
that build was the freshly signed one; key and hashes cut):

```text
# iormeshctl bootstrap app1: the first install of the tracer (decision #1 in docs/decisions.md).
# Verified dist/iormesh-linux-arm64: iormesh-linux-arm64 0.1.0 against the pinned key, sha256 5803dd20….
# The rule names the SSH user ops (-user); the build is for iormesh-linux-arm64 (-arch).
#
# On the controller:
ssh app1 mkdir -p -m 0700 .iormesh/staging
scp dist/iormesh-linux-arm64 dist/iormesh-linux-arm64.minisig app1:.iormesh/staging/
#
# On app1, as ops, on a terminal (sudo may ask for your password):
printf '%s\n' 'untrusted comment: minisign public key: …' … > ~/.iormesh/staging/minisign.pub
printf '%s\n' '# iormesh: installed by iormeshctl bootstrap; iormesh uninstall removes it' 'ops ALL=(root) NOPASSWD: /opt/iormesh/bin/iormesh' > ~/.iormesh/staging/iormesh.sudoers
# create /opt/iormesh, bin/ and etc/, root's 0755
sudo install -d -o root -g root -m 0755 /opt/iormesh /opt/iormesh/bin /opt/iormesh/etc
…
# check the rule's syntax
sudo visudo -cf /etc/sudoers.d/.iormesh.new
# move the sudoers rule into place at /etc/sudoers.d/iormesh
sudo mv -f -T /etc/sudoers.d/.iormesh.new /etc/sudoers.d/iormesh
# the rule runs the tracer without a password
sudo -n /opt/iormesh/bin/iormesh status
```

The cut part installs the tracer and the key, each through a
temporary name and a SHA-256 check against the laptop's, and runs
`sudo /opt/iormesh/bin/iormesh upgrade` on the staged build, which
re-verifies it against the root-owned key and checks the layout.

> **Human step.** Run the `On the controller` lines on the laptop,
> then the rest on `app1` as `ops` in a terminal, where sudo may
> ask for the password. Read the steps before you run them: they
> are the whole install.

**Checkpoint:** the last step, `sudo -n /opt/iormesh/bin/iormesh
status`, runs without a password prompt and, with no session yet,
prints `iormesh: no sessions`. That proves the rule.

### Applied over SSH (sudo without a password)

If `ops` already has sudo without a password, let iormeshctl run
the same steps; it reads the arch and the user from the host:

```sh
iormeshctl bootstrap -apply app2
```

A failed step ends with `…; stopped here, nothing undone`; until
the rule is in place a rerun starts over.

**If not:**

| You see | Do |
|---|---|
| `registry: host not registered: app9 (iormeshctl add it first)` | `add` the host, or check the spelling against `list` |
| `pinned key: open …/minisign.pub: no such file or directory` | make or copy the key ([chapter 3](03-release.md)) |
| `local build, nothing pushed: … signature verification failed: …` | sign `dist/` again ([chapter 3](03-release.md)) |
| `sudo -n failed (…): -apply needs the SSH user's own sudo without a password` | drop `-apply`, run the printed steps by hand |
| `/etc/sudoers.d/iormesh already exists: …` | the host is bootstrapped: go on with `deploy` |

The first three were run for this guide; the rest are in
[troubleshooting.md](../troubleshooting.md#bootstrap).

`-user` defaults to HOST's `user@` part, else your own login
name; a `User` in `~/.ssh/config` is not seen, so name it.

Reference: [decisions.md](../decisions.md) #1 (why the first
install is yours), [security.md](../security.md) (the sudoers
rule and what passes it).

---

← [3. Build and sign a release](03-release.md) · [Contents](README.md) · Next: [5. Deploy and check status](05-deploy-status.md) →

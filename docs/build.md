# Build & code generation

How iormesh is built: the BPF/C code generation it takes from
ior, the local build and test targets, the container builds, the
signing and the release archives. The decisions it cites are in
[decisions.md](decisions.md).

## Parts

This file is the index: each part below is its own file. Read the summaries,
then open only the part you need.

- [Code generation — follow ior](build/codegen.md) — generation from ior, and
  what ior does not give us.
  - [What ior does not give us](build/codegen.md#what-ior-does-not-give-us)
- [Building and testing](build/building-and-testing.md) — the Mage targets,
  prerequisites, CI.
  - [CI](build/building-and-testing.md#ci)
- [Container builds](build/containers.md) — the generation host and container
  builds.
- [Signing and releases](build/signing-releases.md) — operator signing, release
  archives.
  - [Signing](build/signing-releases.md#signing)
  - [Releases](build/signing-releases.md#releases)
- [Tech choices](build/tech-choices.md) — the stack and why.

//go:build mage

// Release archives (PLAN.md "Release archives", docs/build.md "Releases",
// task rh4): Release builds, signs and packs a tagged commit into
// per-arch tarballs plus a signed SHA256SUMS under dist/release/. It
// publishes nothing; uploading the files is a human step. The packing
// and its checks live in internal/dist, where they are unit-tested.

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aead.dev/minisign"
	"github.com/magefile/mage/sh"

	"iormesh/internal/dist"
	"iormesh/internal/version"
)

// releaseExtras are packed into every archive when the repository has
// them: CHANGELOG.md and LICENSE (MIT, task rh4). A missing one is
// noted, not fatal.
var releaseExtras = []string{"CHANGELOG.md", "LICENSE"}

// Release cuts the release of the tagged HEAD with engine (docker or
// podman), e.g. "mage release podman": it refuses a dirty tree or a HEAD
// that is not exactly the tag v<internal/version.Version>, asks for the
// signing key's password up front, then runs every container build, mage
// agentBuild and mage sign's signing, and packs
// dist/release/<version>/iormesh-<version>-<arch>.tar.gz for amd64 and
// arm64 plus SHA256SUMS and SHA256SUMS.minisig. Archive mtimes come from
// $SOURCE_DATE_EPOCH or the commit time, so they are reproducible.
func Release(engine string) error {
	mtime, err := checkReleaseTree()
	if err != nil {
		return err
	}
	key, err := loadSigningKey()
	if err != nil {
		return err
	}
	if err := ContainerBuild(engine, "all"); err != nil {
		return err
	}
	if err := AgentBuild(); err != nil {
		return err
	}
	if err := signDist(key); err != nil {
		return err
	}
	return packRelease(key, mtime)
}

// checkReleaseTree requires a clean HEAD at the version's tag and returns
// the archives' mtime.
func checkReleaseTree() (time.Time, error) {
	describe, err := sh.Output("git", "describe", "--tags", "--dirty", "--match", "v[0-9]*")
	if err != nil {
		return time.Time{}, fmt.Errorf("release: no v%s tag reachable from HEAD: %w", version.Version, err)
	}
	if err := dist.CheckDescribe(describe, version.Version); err != nil {
		return time.Time{}, err
	}
	commitTime, err := sh.Output("git", "log", "-1", "--format=%ct")
	if err != nil {
		return time.Time{}, fmt.Errorf("release: commit time: %w", err)
	}
	return dist.SourceDate(os.Getenv("SOURCE_DATE_EPOCH"), commitTime)
}

// packRelease writes the archives and the signed SHA256SUMS into a fresh
// dist/release/<version>/, so no file of an earlier attempt survives.
func packRelease(key minisign.PrivateKey, mtime time.Time) error {
	pub, ok := key.Public().(minisign.PublicKey)
	if !ok {
		return errors.New("release: key has no minisign public half")
	}
	out := filepath.Join(distDir, "release", version.Version)
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	var extra, missing []string
	for _, name := range releaseExtras {
		if _, err := os.Stat(name); err == nil {
			extra = append(extra, name)
		} else {
			missing = append(missing, name)
		}
	}
	paths, err := dist.Pack(dist.Options{
		DistDir: distDir, OutDir: out, Version: version.Version,
		Arches: tracerArches, PublicKey: pub, ModTime: mtime, Extra: extra,
	})
	if err != nil {
		return err
	}
	sig, err := dist.SignSums(key, out, version.Version, time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("release %s:\n  %s\n", version.Version, strings.Join(append(paths, sig), "\n  "))
	for _, name := range missing {
		fmt.Printf("note: no %s in the repository; the archives carry none\n", name)
	}
	return nil
}

//go:build mage

// Release archives (docs/build.md "Releases", task rh4): Release builds and
// packs a tagged commit into per-arch tarballs plus a SHA256SUMS under
// dist/release/. It signs nothing and needs no key (decision #32, task
// 5m4): each operator signs an unpacked release with their own key
// (`iormeshctl sign`). It publishes nothing; uploading the files is a
// human step. The packing and its checks live in internal/dist, where
// they are unit-tested.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
// that is not exactly the tag v<internal/version.Version>, then runs every
// container build and mage agentBuild, and packs
// dist/release/<version>/iormesh-<version>-<arch>.tar.gz for amd64 and
// arm64 plus SHA256SUMS, with no signature, no key and no password asked
// (decision #32). Every artifact must carry the release version's stamp.
// Archive mtimes come from $SOURCE_DATE_EPOCH or the commit time, so they
// are reproducible.
func Release(engine string) error {
	mtime, err := checkReleaseTree()
	if err != nil {
		return err
	}
	if err := ContainerBuild(engine, "all"); err != nil {
		return err
	}
	if err := AgentBuild(); err != nil {
		return err
	}
	return packRelease(mtime)
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

// packRelease writes the archives and SHA256SUMS into a fresh
// dist/release/<version>/, so no file of an earlier attempt survives.
func packRelease(mtime time.Time) error {
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
		Arches: tracerArches, ModTime: mtime, Extra: extra,
	})
	if err != nil {
		return err
	}
	fmt.Printf("release %s:\n  %s\n", version.Version, strings.Join(paths, "\n  "))
	for _, name := range missing {
		fmt.Printf("note: no %s in the repository; the archives carry none\n", name)
	}
	fmt.Println("the archives are unsigned: operators sign an unpacked one with iormeshctl sign -key KEY DIR")
	return nil
}

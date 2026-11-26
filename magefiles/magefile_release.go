//go:build mage

// Release archives (docs/build.md "Releases", task rh4): Release builds and
// packs a tagged commit into per-arch tarballs plus a SHA256SUMS under
// dist/release/. It signs nothing and needs no key (decision #32, task
// 5m4): each operator signs an unpacked release with their own key
// (`iormeshctl sign`). It publishes nothing; uploading the files is a
// human step. ReleaseUntagged packs an untagged commit the same way into
// dist/untagged/, marked UNTAGGED, for tests such as the M8 agent trial
// (decision #40). The packing and its checks live in internal/dist, where
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
	if err := buildReleaseArtifacts(engine); err != nil {
		return err
	}
	return packRelease(mtime)
}

// ReleaseUntagged packs the clean, untagged HEAD with engine the way
// Release packs a tagged one (decision #40), e.g. "mage releaseUntagged
// podman", for tests that must serve the commit under test as a release
// page would (the M8 agent trial, #37). The same builds, stamp checks and
// archive names as Release — the artifacts carry internal/version's
// stamp, which allows MAJOR.MINOR.PATCH only — but into
// dist/untagged/<version>/, never dist/release/, with an UNTAGGED note
// naming the commit next to SHA256SUMS and inside every archive. It
// refuses a dirty tree and a HEAD at the release tag; Release still
// refuses every untagged commit.
func ReleaseUntagged(engine string) error {
	note, mtime, err := checkUntaggedTree()
	if err != nil {
		return err
	}
	if err := buildReleaseArtifacts(engine); err != nil {
		return err
	}
	return packUntagged(note, mtime)
}

// buildReleaseArtifacts runs every container build and mage agentBuild:
// the dist/ both release targets pack.
func buildReleaseArtifacts(engine string) error {
	if err := ContainerBuild(engine, "all"); err != nil {
		return err
	}
	return AgentBuild()
}

// checkUntaggedTree requires a clean HEAD that is not the version's tag
// and returns the UNTAGGED note and the archives' mtime.
func checkUntaggedTree() ([]byte, time.Time, error) {
	describe, err := sh.Output("git", "describe", "--tags", "--dirty", "--always", "--match", "v[0-9]*")
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("untagged build: git describe: %w", err)
	}
	if err := dist.CheckUntagged(describe, version.Version); err != nil {
		return nil, time.Time{}, err
	}
	commit, err := sh.Output("git", "rev-parse", "HEAD")
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("untagged build: git rev-parse: %w", err)
	}
	note, err := dist.UntaggedNote(commit, describe, version.Version)
	if err != nil {
		return nil, time.Time{}, err
	}
	mtime, err := sourceDate()
	return note, mtime, err
}

// packUntagged writes the UNTAGGED note into a fresh
// dist/untagged/<version>/ and packs the archives there with the note as
// one more extra, so it lands in every archive too.
func packUntagged(note []byte, mtime time.Time) error {
	out := filepath.Join(distDir, "untagged", version.Version)
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	marker := filepath.Join(out, dist.UntaggedFile)
	if err := os.WriteFile(marker, note, 0o644); err != nil {
		return err
	}
	if err := packArchives(out, mtime, marker); err != nil {
		return err
	}
	first, _, _ := strings.Cut(string(note), "\n")
	fmt.Println(first)
	return nil
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
	return sourceDate()
}

// sourceDate is the build's fixed time (dist.SourceDate):
// $SOURCE_DATE_EPOCH, else HEAD's commit time. The release archives' mtimes
// and the jars' entry dates (jarCreate) both come from it, so a rebuild of
// the same commit gives the same bytes. Without git (a source tarball)
// SOURCE_DATE_EPOCH must be set.
func sourceDate() (time.Time, error) {
	epoch := os.Getenv("SOURCE_DATE_EPOCH")
	if epoch != "" {
		return dist.SourceDate(epoch, "")
	}
	commitTime, err := sh.Output("git", "log", "-1", "--format=%ct")
	if err != nil {
		return time.Time{}, fmt.Errorf("no commit time (git log: %w); set SOURCE_DATE_EPOCH", err)
	}
	return dist.SourceDate(epoch, commitTime)
}

// jarCreate runs `jar --create` with args, every entry stamped with date
// (sourceDate) instead of the build time (task bm4): the jar tool sorts
// the entries itself, so the same classes give the same jar. Every jar
// the magefiles pack goes through it.
func jarCreate(date time.Time, args ...string) error {
	return sh.RunV("jar", append([]string{"--create", "--date", dist.JarDate(date)}, args...)...)
}

// packRelease writes the archives and SHA256SUMS into a fresh
// dist/release/<version>/, so no file of an earlier attempt survives.
func packRelease(mtime time.Time) error {
	out := filepath.Join(distDir, "release", version.Version)
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	return packArchives(out, mtime)
}

// packArchives packs dist/ into out with the releaseExtras the
// repository has, then more (ReleaseUntagged's note), and prints what it
// wrote.
func packArchives(out string, mtime time.Time, more ...string) error {
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
		Arches: tracerArches, ModTime: mtime, Extra: append(extra, more...),
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

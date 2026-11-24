// Package containerbuild holds the container builds of the release
// binaries as data: which builder each target uses, the engine command
// lines that build the builder images and run the builds, and the shell
// script each build runs inside its container (docs/build.md
// "Container builds").
//
// Magefile.go is behind `//go:build mage`, so nothing can test it; keeping
// the argv here (as internal/gatecmd does for the lint gates) makes it
// ordinary Go with unit tests. magefile_container.go is the thin wrapper
// that checks the engine, executes these command lines and verifies the
// binaries they leave in dist/.
//
// Docker and Podman take the same arguments for everything used here, so
// the engine is just argv[0]; the one difference that matters - who owns
// the files a build writes to the host - is handled in the build script.
package containerbuild

import (
	"fmt"
	"slices"
	"strings"
)

// The supported engines. Both are driven with the same flags.
const (
	Docker = "docker"
	Podman = "podman"
)

// Pinned upstream images. The builder Containerfiles in scripts/container
// name the same references (a test keeps them in step): GolangImage is the
// Go toolchain of every build and the controller's builder as is;
// RockyImage is the tracer's first target platform, which the smoke test
// runs each Linux binary on. Both are multi-arch index digests, so
// `--platform linux/arm64` resolves to the arm64 image of the same pin.
const (
	GolangImage = "docker.io/library/golang:1.26.10-trixie" +
		"@sha256:fd9a91f0ece5664556f4851d06dce5e5b505a71166dffd385a03ba9d200b6983"
	RockyImage = "docker.io/rockylinux/rockylinux:9-minimal" +
		"@sha256:96aef88435673089451d3ca68780d3f68936f19a03b15b685bff54e55b6ef3a5"
)

// Paths and names shared with magefile_container.go.
const (
	// DistDir is where every release binary lands, on the host and in the
	// container's copy of the tree alike.
	DistDir = "dist"
	// ContainerfileDir holds the builder Containerfiles; it is also the
	// (tiny) build context, since the images carry no source.
	ContainerfileDir = "scripts/container"
	// GoModVolume and GoCacheVolume are named volumes that keep module
	// downloads and the build cache across runs; ContainerClean removes them.
	GoModVolume   = "iormesh-gomod"
	GoCacheVolume = "iormesh-gocache"
	// TargetArchEnv selects the tracer's GOARCH and BPF __TARGET_ARCH for
	// `mage releaseTracer` (default amd64).
	TargetArchEnv = "IORMESH_TARGET_ARCH"
	// VMLinuxEnv names a vmlinux.h for the arm64 build (see RunOptions).
	VMLinuxEnv = "IORMESH_VMLINUX_ARM64"
)

// Target is one container build: the builder it runs in and the release
// binaries it leaves in dist/.
type Target struct {
	// Name is how the target is selected (`mage containerBuild ENGINE NAME`).
	Name string
	// Image is the builder image the build runs in: a local tag built from
	// Containerfile, or a pinned upstream image used as is.
	Image string
	// Containerfile is relative to the repo root; empty when Image is used
	// as is.
	Containerfile string
	// MageTarget is the `mage` target the container runs on its copy of
	// the tree, with Env set.
	MageTarget string
	Env        map[string]string
	// Binaries are the files the build writes to dist/.
	Binaries []Binary
	// Tracer marks the builds that need the host's BTF mounted (vmlinux.h).
	Tracer bool
}

// Binary is one release binary and the platform it must be built for.
type Binary struct {
	Name   string
	GOOS   string
	GOARCH string
	// Static binaries must have no ELF interpreter: the tracer is copied
	// to hosts whose libc is not the builder's.
	Static bool
}

// TracerBinary and CtlBinary name the release binaries in dist/.
func TracerBinary(goarch string) string { return "iormesh-linux-" + goarch }

// CtlBinary is the controller's name for goos/goarch.
func CtlBinary(goos, goarch string) string {
	return "iormeshctl-" + goos + "-" + goarch
}

// CtlPlatforms are the controller's release platforms: macOS and Linux
// laptops, both arches (docs/milestones.md M4).
var CtlPlatforms = [][2]string{
	{"darwin", "amd64"}, {"darwin", "arm64"},
	{"linux", "amd64"}, {"linux", "arm64"},
}

// Targets lists every container build in the order `mage buildDocker` and
// `mage buildPodman` run them: the quick controller first, then the
// tracers.
func Targets() []Target {
	return []Target{ctlTarget(), tracerTarget("amd64"), tracerTarget("arm64")}
}

func ctlTarget() Target {
	var bins []Binary
	for _, p := range CtlPlatforms {
		bins = append(bins, Binary{Name: CtlBinary(p[0], p[1]), GOOS: p[0], GOARCH: p[1]})
	}
	return Target{Name: "ctl", Image: GolangImage, MageTarget: "releaseCtl", Binaries: bins}
}

func tracerTarget(goarch string) Target {
	name := "tracer-" + goarch
	return Target{
		Name:          name,
		Image:         "localhost/iormesh-builder:" + name,
		Containerfile: ContainerfileDir + "/" + name + ".Containerfile",
		MageTarget:    "releaseTracer",
		Env:           map[string]string{TargetArchEnv: goarch},
		Binaries: []Binary{{Name: TracerBinary(goarch), GOOS: "linux",
			GOARCH: goarch, Static: true}},
		Tracer: true,
	}
}

// Select resolves a comma-separated list of target names; "" and "all"
// select every target. Unknown names fail with the valid ones listed.
func Select(names string) ([]Target, error) {
	all := Targets()
	if names == "" || names == "all" {
		return all, nil
	}
	var picked []Target
	for _, name := range strings.Split(names, ",") {
		i := slices.IndexFunc(all, func(t Target) bool { return t.Name == strings.TrimSpace(name) })
		if i < 0 {
			return nil, fmt.Errorf("unknown container build target %q (valid: all, %s)",
				name, strings.Join(targetNames(all), ", "))
		}
		picked = append(picked, all[i])
	}
	return picked, nil
}

func targetNames(targets []Target) []string {
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = t.Name
	}
	return names
}

// CheckEngine rejects anything but docker and podman before it is exec'd.
func CheckEngine(engine string) error {
	if engine != Docker && engine != Podman {
		return fmt.Errorf("unknown container engine %q (use %s or %s)", engine, Docker, Podman)
	}
	return nil
}

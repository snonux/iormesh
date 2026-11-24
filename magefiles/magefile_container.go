//go:build mage

// Container builds of the release binaries (docs/build.md "Container
// builds"): the host side - BuildDocker,
// BuildPodman, ContainerBuild, ContainerClean - runs the engine; the
// in-container side - ReleaseTracer, ReleaseCtl - is ordinary mage targets
// that also work on a host with the right toolchain. The command lines live
// in internal/containerbuild, where they are unit-tested.

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"

	"iormesh/internal/containerbuild"
)

// tracerArches are the tracer's release arches; crossCC is the C cross
// compiler cgo and libbpf use for an arch other than the build host's (the
// arm64 builder's Debian package gcc-aarch64-linux-gnu).
var (
	tracerArches = []string{"amd64", "arm64"}
	crossCC      = map[string]string{"arm64": "aarch64-linux-gnu-gcc"}
	// binfmtNames are the binfmt_misc entries qemu-user-static registers;
	// one present means the engine can run that arch's images emulated.
	binfmtNames = map[string]string{"amd64": "qemu-x86_64", "arm64": "qemu-aarch64"}
)

// BuildDocker builds every release binary into dist/ with Docker: the
// controller for darwin/linux x amd64/arm64 and the static tracer for
// linux/amd64 and linux/arm64, each verified and smoke-run on Rocky 9.
func BuildDocker() error {
	return ContainerBuild(containerbuild.Docker, "all")
}

// BuildPodman is BuildDocker with Podman (rootless works).
func BuildPodman() error {
	return ContainerBuild(containerbuild.Podman, "all")
}

// ContainerBuild builds the comma-separated targets (ctl, tracer-amd64,
// tracer-arm64, or all) with engine (docker or podman), e.g.
// "mage containerBuild podman tracer-amd64,ctl".
func ContainerBuild(engine, targets string) error {
	if err := containerbuild.CheckEngine(engine); err != nil {
		return err
	}
	selected, err := containerbuild.Select(targets)
	if err != nil {
		return err
	}
	if err := checkEngineUsable(engine); err != nil {
		return err
	}
	opts, err := containerRunOptions()
	if err != nil {
		return err
	}
	for _, t := range selected {
		if err := runContainerTarget(engine, t, opts); err != nil {
			return fmt.Errorf("container build %s: %w", t.Name, err)
		}
	}
	return nil
}

// ContainerClean removes what the container builds leave in the engine:
// the tracer builder images and the Go module and build-cache volumes.
// Missing ones are reported and skipped, so it can run any time.
func ContainerClean(engine string) error {
	if err := containerbuild.CheckEngine(engine); err != nil {
		return err
	}
	if err := checkEngineUsable(engine); err != nil {
		return err
	}
	var argvs [][]string
	for _, t := range containerbuild.Targets() {
		if t.Containerfile != "" {
			argvs = append(argvs, containerbuild.CleanImageArgv(engine, t.Image))
		}
	}
	for _, v := range []string{containerbuild.GoModVolume, containerbuild.GoCacheVolume} {
		argvs = append(argvs, containerbuild.CleanVolumeArgv(engine, v))
	}
	for _, argv := range argvs {
		if err := sh.RunV(argv[0], argv[1:]...); err != nil {
			fmt.Printf("skipped: %s: %v\n", strings.Join(argv, " "), err)
		}
	}
	return nil
}

// ReleaseTracer links the static tracer for $IORMESH_TARGET_ARCH (default
// amd64) into dist/iormesh-linux-<arch>, with the BPF object compiled for
// that arch embedded. The container builds run it; on a host it needs the
// static libelf/libz/libzstd/glibc archives (and, for arm64, the cross
// compiler) the builder images carry. netgo and osusergo keep the static
// glibc link free of NSS lookups, which cannot work statically.
func ReleaseTracer() error {
	arch, err := targetArch()
	if err != nil {
		return err
	}
	mg.Deps(BpfBuild)
	bin := containerbuild.Binary{Name: containerbuild.TracerBinary(arch),
		GOOS: "linux", GOARCH: arch, Static: true}
	out := filepath.Join(containerbuild.DistDir, bin.Name)
	if err := sh.RunWithV(goEnv(), "go", "build", "-tags", "netgo,osusergo", "-trimpath",
		"-ldflags", `-w -extldflags "-static"`, "-o", out, "./cmd/iormesh"); err != nil {
		return err
	}
	return containerbuild.VerifyBinary(out, bin)
}

// ReleaseCtl cross-compiles the cgo-free controller for every platform in
// containerbuild.CtlPlatforms into dist/iormeshctl-<os>-<arch>. Pure Go,
// so any host with Go can run it; the container build runs it on the
// pinned golang image.
func ReleaseCtl() error {
	for _, p := range containerbuild.CtlPlatforms {
		bin := containerbuild.Binary{Name: containerbuild.CtlBinary(p[0], p[1]),
			GOOS: p[0], GOARCH: p[1]}
		out := filepath.Join(containerbuild.DistDir, bin.Name)
		env := map[string]string{"CGO_ENABLED": "0", "GOOS": p[0], "GOARCH": p[1]}
		if err := sh.RunWithV(env, "go", "build", "-trimpath", "-ldflags", "-w",
			"-o", out, "./cmd/iormeshctl"); err != nil {
			return err
		}
		if err := containerbuild.VerifyBinary(out, bin); err != nil {
			return err
		}
	}
	return nil
}

// targetArch is the tracer's target arch from $IORMESH_TARGET_ARCH,
// amd64 when unset (the dev host's arch, and what every non-release target
// builds for).
func targetArch() (string, error) {
	arch := os.Getenv(containerbuild.TargetArchEnv)
	if arch == "" {
		return "amd64", nil
	}
	for _, a := range tracerArches {
		if a == arch {
			return arch, nil
		}
	}
	return "", fmt.Errorf("%s=%q: the tracer builds for %s",
		containerbuild.TargetArchEnv, arch, strings.Join(tracerArches, ", "))
}

// checkEngineUsable fails with the fix spelled out when engine is missing
// or installed but unusable (Docker's CLI without a running daemon).
func checkEngineUsable(engine string) error {
	if _, err := exec.LookPath(engine); err != nil {
		return fmt.Errorf("%s is not installed (not on PATH): install it, or build with the other "+
			"engine (mage buildDocker / mage buildPodman)", engine)
	}
	// Only stderr is kept: `docker info` prints the whole client section to
	// stdout before the one line that says why the server is unreachable.
	argv := containerbuild.InfoArgv(engine)
	cmd := exec.Command(argv[0], argv[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s is installed but not usable (is its daemon running?): `%s` failed: %w\n%s",
			engine, strings.Join(argv, " "), err, bytes.TrimSpace(stderr.Bytes()))
	}
	return nil
}

// containerRunOptions collects the host paths a build mounts. dist/ is
// created here, so the engine does not create it root-owned.
func containerRunOptions() (containerbuild.RunOptions, error) {
	root, err := filepath.Abs(".")
	if err != nil {
		return containerbuild.RunOptions{}, err
	}
	dist := filepath.Join(root, containerbuild.DistDir)
	if err := os.MkdirAll(dist, 0o755); err != nil {
		return containerbuild.RunOptions{}, err
	}
	opts := containerbuild.RunOptions{RepoRoot: root, Dist: dist,
		HostUID: os.Getuid(), HostGID: os.Getgid()}
	if vmlinux := os.Getenv(containerbuild.VMLinuxEnv); vmlinux != "" {
		if opts.VMLinux, err = filepath.Abs(vmlinux); err != nil {
			return opts, err
		}
		if _, err := os.Stat(opts.VMLinux); err != nil {
			return opts, fmt.Errorf("%s: %w", containerbuild.VMLinuxEnv, err)
		}
	}
	return opts, nil
}

// runContainerTarget builds t's builder image (layer-cached after the first
// run), runs the build, then checks each binary it left in dist/: header
// first, then a --help smoke run on Rocky 9. Old binaries are removed
// first so a build that fails to copy one cannot pass on a stale file.
func runContainerTarget(engine string, t containerbuild.Target, o containerbuild.RunOptions) error {
	if argv := containerbuild.BuildImageArgv(engine, t); argv != nil {
		if err := sh.RunV(argv[0], argv[1:]...); err != nil {
			return err
		}
	}
	if t.Env[containerbuild.TargetArchEnv] == "arm64" && o.VMLinux == "" {
		fmt.Printf("note: %s compiles against the build host's vmlinux.h; set %s to an "+
			"arm64 host's dump to use arm64 kernel types (docs/build.md)\n",
			t.Name, containerbuild.VMLinuxEnv)
	}
	for _, b := range t.Binaries {
		if err := removeFilesByPath(filepath.Join(o.Dist, b.Name)); err != nil {
			return err
		}
	}
	argv := containerbuild.RunArgv(engine, t, o)
	if err := sh.RunV(argv[0], argv[1:]...); err != nil {
		return err
	}
	for _, b := range t.Binaries {
		if err := checkReleaseBinary(engine, b, o.Dist); err != nil {
			return err
		}
	}
	return nil
}

// checkReleaseBinary verifies b's header and smoke-runs a Linux binary on
// Rocky 9 of its own arch. A foreign arch runs only when the host emulates
// it; otherwise the run is skipped, loudly. darwin binaries get the header
// check alone.
func checkReleaseBinary(engine string, b containerbuild.Binary, dist string) error {
	if err := containerbuild.VerifyBinary(filepath.Join(dist, b.Name), b); err != nil {
		return err
	}
	if b.GOOS != "linux" {
		return nil
	}
	if b.GOARCH != runtime.GOARCH && !canEmulate(b.GOARCH) {
		fmt.Printf("SKIPPED smoke run of %s: this host cannot run %s images "+
			"(install qemu-user-static for binfmt_misc %s)\n", b.Name, b.GOARCH, binfmtNames[b.GOARCH])
		return nil
	}
	argv := containerbuild.SmokeArgv(engine, b, dist)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("smoke run `%s`: %w", strings.Join(argv, " "), err)
	}
	if err := containerbuild.CheckSmokeOutput(b.Name, string(out)); err != nil {
		return err
	}
	fmt.Printf("smoke run ok: %s --help on Rocky 9 linux/%s\n", b.Name, b.GOARCH)
	return nil
}

// canEmulate reports whether binfmt_misc has qemu-user registered for
// goarch, which is what lets the engine run that arch's images.
func canEmulate(goarch string) bool {
	name, ok := binfmtNames[goarch]
	if !ok {
		return false
	}
	_, err := os.Stat(filepath.Join("/proc/sys/fs/binfmt_misc", name))
	return err == nil
}

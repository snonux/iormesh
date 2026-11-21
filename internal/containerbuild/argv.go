package containerbuild

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Mount points inside the build containers.
const (
	srcMount     = "/src"
	outMount     = "/out"
	buildDir     = "/build"
	goModMount   = "/gomod"
	goCacheMount = "/gocache"
	btfDir       = "/sys/kernel/btf"
	vmlinuxMount = "/vmlinux.h"
)

// builderPlatform is the platform the tracer builders are built for and
// run on. The arm64 tracer is cross-compiled, not emulated, so both tracer
// builders are amd64 images whatever the target arch.
const builderPlatform = "linux/amd64"

// RunOptions are the host-side inputs of one build run.
type RunOptions struct {
	// RepoRoot is the absolute path of the source tree, mounted read-only;
	// the build works on a copy so it never writes into the tree (no
	// root-owned files, no clobbered internal/c/iormesh.bpf.o).
	RepoRoot string
	// Dist is the absolute path of the host's dist/ directory.
	Dist string
	// VMLinux, when set, is a vmlinux.h the arm64 tracer build uses
	// instead of the one in the tree or the host's BTF dump: an arm64
	// host's dump (see docs/build.md). Other builds ignore it.
	VMLinux string
	// HostUID and HostGID own the binaries a Docker build writes. Docker
	// runs the build as real root; rootless Podman's root already is the
	// invoking user, and chown'ing there would hand the files to a subuid.
	HostUID, HostGID int
}

// InfoArgv probes whether the engine is usable: for Docker this fails when
// the daemon is not running even though the CLI is installed.
func InfoArgv(engine string) []string {
	return []string{engine, "info"}
}

// BuildImageArgv builds t's builder image, or returns nil when t runs an
// upstream image as is. The build context is the Containerfile directory:
// the images carry no source, so the repo is not sent to the engine.
func BuildImageArgv(engine string, t Target) []string {
	if t.Containerfile == "" {
		return nil
	}
	return []string{engine, "build", "--platform", builderPlatform,
		"-f", t.Containerfile, "-t", t.Image, ContainerfileDir}
}

// RunArgv runs t's build in its builder.
//
// SELinux labeling is disabled for the run instead of relabeling the
// mounts (`:z`), which would rewrite the labels of the user's source tree
// and cannot relabel /sys. The host's BTF is mounted for the tracers so
// vmlinux.h can be dumped when the tree has none; it is world-readable,
// so rootless Podman can read it too, and no privileges are needed: the
// build never loads BPF or reads tracefs (the generated sources are
// committed - see Script).
func RunArgv(engine string, t Target, o RunOptions) []string {
	argv := []string{engine, "run", "--rm", "--security-opt", "label=disable"}
	if t.Tracer {
		argv = append(argv, "--platform", builderPlatform)
	}
	argv = append(argv,
		"-v", o.RepoRoot+":"+srcMount+":ro",
		"-v", o.Dist+":"+outMount,
		"-v", GoModVolume+":"+goModMount,
		"-v", GoCacheVolume+":"+goCacheMount,
		"-e", "GOMODCACHE="+goModMount,
		"-e", "GOCACHE="+goCacheMount,
		"-e", "GOFLAGS=-buildvcs=false")
	if t.Tracer {
		argv = append(argv, "-v", btfDir+":"+btfDir+":ro")
		if usesVMLinux(t, o) {
			argv = append(argv, "-v", o.VMLinux+":"+vmlinuxMount+":ro")
		}
	}
	for _, k := range slices.Sorted(maps.Keys(t.Env)) {
		argv = append(argv, "-e", k+"="+t.Env[k])
	}
	return append(argv, t.Image, "sh", "-c", Script(engine, t, o))
}

// Script is the shell the build container runs.
//
// It copies the read-only tree (without .git, other worktrees and dist/)
// and runs the target's mage target on the copy through `go run`, so mage
// is the version go.mod pins and go.sum checks, and no builder image has to
// carry it (go.mod needs v1.17+: older mage pastes the magefiles' doc
// comments into a raw string literal and breaks on their backquotes). It
// does not run `mage generate`: the
// generated handlers and tables are committed, pinned by the diff gate on
// the generation host, and generation would need the host's tracefs (root)
// - an arm64 cross build could not read its target's tracepoints anyway.
func Script(engine string, t Target, o RunOptions) string {
	lines := []string{
		"set -eu",
		"mkdir -p " + buildDir,
		fmt.Sprintf("tar -C %s --exclude=./.git --exclude=./.claude --exclude=./%s -cf - . | tar -C %s -xf -",
			srcMount, DistDir, buildDir),
		"cd " + buildDir,
	}
	if usesVMLinux(t, o) {
		lines = append(lines, "cp "+vmlinuxMount+" internal/c/vmlinux.h")
	}
	lines = append(lines, "go run github.com/magefile/mage "+t.MageTarget)
	for _, b := range t.Binaries {
		lines = append(lines, fmt.Sprintf("cp %s/%s %s/", DistDir, b.Name, outMount))
		if engine == Docker {
			lines = append(lines, fmt.Sprintf("chown %d:%d %s/%s", o.HostUID, o.HostGID, outMount, b.Name))
		}
	}
	return strings.Join(lines, "\n")
}

// usesVMLinux reports whether o's vmlinux.h goes into t's build: only the
// arm64 tracer takes one, since the amd64 builds compile against the
// amd64 host's own BTF, which is the right one for them.
func usesVMLinux(t Target, o RunOptions) bool {
	return t.Tracer && t.Env[TargetArchEnv] == "arm64" && o.VMLinux != ""
}

// SmokeArgv runs a Linux release binary's --help on Rocky Linux 9 (the
// tracer's first target) of the binary's own arch. An arm64 run needs the
// host to emulate arm64 (binfmt_misc with qemu-aarch64); the caller checks.
func SmokeArgv(engine string, b Binary, dist string) []string {
	return []string{engine, "run", "--rm", "--security-opt", "label=disable",
		"--platform", "linux/" + b.GOARCH,
		"-v", dist + ":/dist:ro", RockyImage, "/dist/" + b.Name, "--help"}
}

// CleanImageArgv removes one builder image; CleanVolumeArgv one cache
// volume.
func CleanImageArgv(engine, image string) []string {
	return []string{engine, "rmi", image}
}

// CleanVolumeArgv removes one of the named cache volumes.
func CleanVolumeArgv(engine, volume string) []string {
	return []string{engine, "volume", "rm", volume}
}

// SelinuxDir is the iormesh-selinux packaging directory (task 7h4) and
// SelinuxDist where SelinuxRPMArgv's build puts the RPMs, both relative
// to the repository root.
const (
	SelinuxDir  = "packaging/selinux"
	SelinuxDist = DistDir + "/selinux"
)

// SelinuxRPMArgv builds the iormesh-selinux RPM in a throwaway Rocky
// Linux 9 container: src (the absolute packaging/selinux) read-only at
// /src, out (absolute) at /out, packaging/selinux/build.sh as the
// command. The build installs its requirements with microdnf, so it runs
// as the container's root; under Docker, which is real root, owner
// (uid:gid) gets the RPMs back, while rootless Podman's root already is
// the invoking user (owner is ignored, as in RunOptions).
func SelinuxRPMArgv(engine, src, out, owner string) []string {
	argv := []string{engine, "run", "--rm", "--security-opt", "label=disable",
		"--platform", builderPlatform,
		"-v", src + ":" + srcMount + ":ro", "-v", out + ":" + outMount}
	if engine == Docker && owner != "" {
		argv = append(argv, "-e", "IORMESH_OUT_OWNER="+owner)
	}
	return append(argv, RockyImage, "bash", srcMount+"/build.sh", srcMount, outMount)
}

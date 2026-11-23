//go:build mage

// Adapted from ior Magefile.go @ b21afc50328c.
// Changes: only the build, BPF, generate, test, vet, lint, fmt and clean
// targets, plus TestRace (task ij4) (no TUI, demo, bench, parquet or docker
// targets; the integration targets are in magefile_integration.go);
// IOR_FORCE_GENERATE renamed to IORMESH_FORCE_GENERATE; Build compiles every
// package instead of a binary, since cmd/iormesh lands in M1 step 10, and
// links the cgo-free controller ./iormeshctl beside the tracer;
// BpfRootTest is ior's runRootLinkTests/runRootTestBinary, reduced to the BPF
// root tests of ./internal; GOARCH, the cgo cross compiler and the BPF
// __TARGET_ARCH follow $IORMESH_TARGET_ARCH for the arm64 release build
// (magefile_container.go replaces ior's docker targets).

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"

	"iormesh/internal/containerbuild"
	"iormesh/internal/gatecmd"
)

// Build-input and output paths of the BPF and generation targets. They
// mirror ior's layout so ior fixes port file for file.
const (
	defaultLibbpfgoPath  = "../libbpfgo"
	libbpfgoRequiredTag  = "v0.9.2-libbpf-1.5.1"
	bpfSourcePath        = "internal/c/iormesh.bpf.c"
	bpfObjectPath        = "internal/c/iormesh.bpf.o"
	tracepointsCPath     = "internal/c/generated_tracepoints.c"
	tracepointsResult    = "internal/c/generated_tracepoints_result.txt"
	tracepointsResultNew = "internal/c/generated_tracepoints_result.txt.new"
	tracepointsGoPath    = "internal/tracepoints/generated_tracepoints.go"
	typesGoPath          = "internal/types/generated_types.go"
	typesHeaderPath      = "internal/c/types.h"
	vmlinuxPath          = "internal/c/vmlinux.h"
	forceGenerateEnv     = "IORMESH_FORCE_GENERATE"
	rootTestBinary       = buildDir + "/iormesh-internal.test"
	binaryName           = buildDir + "/iormesh"
	ctlBinaryName        = buildDir + "/iormeshctl"
)

// Default builds the project.
func Default() {
	mg.Deps(Build)
}

// Build compiles the BPF object and every Go package with the libbpfgo cgo
// environment, then links the tracer binary bin/iormesh (cmd/iormesh, M1
// step 10) with the object embedded, and the controller bin/iormeshctl
// (cmd/iormeshctl, M4) with CGO_ENABLED=0: built the way it ships, so a cgo
// dependency fails here and not first on a Mac.
func Build() error {
	mg.Deps(BpfBuild)
	if err := sh.RunWithV(goEnv(), "go", "build", "./..."); err != nil {
		return err
	}
	if err := sh.RunWithV(goEnv(), "go", "build", "-o", binaryName, "./cmd/iormesh"); err != nil {
		return err
	}
	return sh.RunWithV(map[string]string{"CGO_ENABLED": "0"},
		"go", "build", "-o", ctlBinaryName, "./cmd/iormeshctl")
}

// BpfBuild builds the embedded BPF object (CO-RE, from vmlinux.h).
func BpfBuild() error {
	if err := ensureVMLINUX(); err != nil {
		return err
	}
	return buildBPFObject()
}

// Test runs the full Go test suite (not the Java workload; see WorkloadTest).
func Test() error {
	mg.Deps(BpfBuild)
	if err := run(gatecmd.CleanTestCache()); err != nil {
		return err
	}
	return run(gatecmd.TestAll())
}

// TestRace runs the suite under the race detector (task ij4): every package
// `go list ./...` names except gatecmd.RaceExcluded, the two whose -race
// cost dwarfs what they could find (docs/testing.md, "Race detector gate").
// $IORMESH_RACE_PKGS (space-separated) replaces the package set, to race-check
// one package while working on it.
func TestRace() error {
	mg.Deps(BpfBuild)
	pkgs := strings.Fields(os.Getenv(gatecmd.RacePackagesEnv))
	if len(pkgs) == 0 {
		argv := gatecmd.ListPackages()
		listed, err := sh.OutputWith(goEnv(), argv[0], argv[1:]...)
		if err != nil {
			return err
		}
		pkgs = gatecmd.RacePackages(strings.Fields(listed))
	}
	return run(gatecmd.TestRace(pkgs))
}

// BpfRootTest runs the BPF root tests of ./internal (gatecmd.RootBPFTests)
// as root: their test binary is built with the cgo environment, run through
// `sudo -n -E` unless mage already is root, and removed again however the
// run ends. The exit status alone is not believed - skipped or unmatched
// tests exit 0 too - so every listed test must be reported as passed.
func BpfRootTest() (err error) {
	mg.Deps(BpfBuild)
	defer func() {
		err = errors.Join(err, removeFilesByPath(rootTestBinary))
	}()
	if err := sh.RunWithV(goEnv(), "go", "test", "-c", "./internal/", "-o", rootTestBinary); err != nil {
		return err
	}
	var output bytes.Buffer
	if err := runRootTestBinary(rootTestBinary, "internal", io.MultiWriter(os.Stdout, &output), gatecmd.RootBPFTestArgs()...); err != nil {
		return err
	}
	if missing := gatecmd.RootBPFTestsNotPassed(output.String()); len(missing) != 0 {
		return fmt.Errorf("the test binary exited 0 but did not report these tests as passed: %s",
			strings.Join(missing, ", "))
	}
	return nil
}

// runRootTestBinary execs the test binary at binary (relative to the repo
// root) as root in dir, the package directory, as `go test` would. stderr
// passes through; stdout goes to the caller's writer so the verdicts can be
// read back. BpfRootTest and the integration targets
// (magefile_integration.go) share it.
func runRootTestBinary(binary, dir string, stdout io.Writer, args ...string) error {
	binaryPath, err := filepath.Abs(binary)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", binary, err)
	}
	argv := append([]string{binaryPath}, args...)
	if os.Geteuid() != 0 {
		argv = append([]string{"sudo", "-n", "-E"}, argv...)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Vet runs go vet over the module with the libbpfgo cgo environment; a bare
// `go vet ./...` cannot typecheck the packages that import libbpfgo.
func Vet() error {
	mg.Deps(BpfBuild)
	return run(gatecmd.VetAll())
}

// Lint runs golangci-lint (errcheck + staticcheck SA, see .golangci.yml)
// with the same cgo environment, after verifying the config: `run` silently
// ignores unknown keys and would report "0 issues" from an unreviewed file.
func Lint() error {
	mg.Deps(BpfBuild)
	if _, err := exec.LookPath(gatecmd.GolangciLintBin); err != nil {
		return fmt.Errorf("%s not on PATH; install it with `go install %s`",
			gatecmd.GolangciLintBin, gatecmd.GolangciLintPkg)
	}
	if err := run(gatecmd.LintConfigVerify()); err != nil {
		return err
	}
	return run(gatecmd.LintRun())
}

// run executes one gate command line with the libbpfgo cgo environment. The
// argv comes from internal/gatecmd so internal/buildgate can pin it.
func run(argv []string) error {
	return sh.RunWithV(goEnv(), argv[0], argv[1:]...)
}

// Clean removes build artifacts: the BPF object and the generate
// transients, and bin/ (decision #30) - the tracer and controller
// binaries, the Go peers, the jars and the compiled test binaries - plus
// any of those a tree built before the move still has at the root
// (cleanBuildOutputs). vmlinux.h is deliberately
// preserved: it is a gitignored build input recoverable only with root
// (ensureVMLINUX dumps it via sudo bpftool), so deleting it would leave
// the tree unbuildable on a host without sudo. dist/ is left alone: it
// holds the signed release artifacts, not intermediates.
func Clean() error {
	// The *.tmp-* glob sweeps generate temp renders orphaned by a signal
	// between render and the atomic rename.
	for _, pattern := range []string{"internal/c/*.o", tracepointsCPath + ".tmp-*"} {
		if err := removeFilesByGlob(pattern); err != nil {
			return err
		}
	}
	if err := cleanBuildOutputs(); err != nil {
		return err
	}
	return removeFilesByPath(tracepointsResultNew)
}

// cleanBuildOutputs removes buildDir whole, then the same outputs by their
// base names at the repository root, where every target wrote them before
// decision #30 moved them (tasks rk4/uk4): a checkout built earlier is
// swept clean too. The root *.test glob catches the integration and
// root-BPF test binaries of such a tree and any stale one.
func cleanBuildOutputs() error {
	if err := os.RemoveAll(buildDir); err != nil {
		return fmt.Errorf("remove %s: %w", buildDir, err)
	}
	for _, path := range []string{
		binaryName, ctlBinaryName, echoPeerBinary, edgePeerBinary,
		workloadJar, libsJar, otelJar, agentJar,
	} {
		if err := removeFilesByPath(filepath.Base(path)); err != nil {
			return err
		}
	}
	return removeFilesByGlob("*.test")
}

func buildBPFObject() error {
	if err := ensureLibbpfgoStaticToolchain(); err != nil {
		return err
	}
	// The BPF bytecode is the same for every host arch, but __TARGET_ARCH
	// selects the arch-specific paths (dispatch.c's compat-task check), so
	// it follows the tracer's GOARCH.
	arch, err := targetArch()
	if err != nil {
		return err
	}
	includeDir := filepath.Join(libbpfgoPath(), "output")
	return sh.RunWithV(map[string]string{"CC": "clang"}, "clang", "-g", "-O2",
		"-Wall", "-fpie", "-target", "bpf", "-D__TARGET_ARCH_"+arch,
		"-I"+includeDir, "-c", bpfSourcePath, "-o", bpfObjectPath)
}

// ensureLibbpfgoStaticToolchain fails early, with the fix spelled out, when
// the pinned libbpfgo checkout has not been built: the clang and cgo errors
// it would otherwise cause (bpf/bpf.h not found) do not name the cause.
func ensureLibbpfgoStaticToolchain() error {
	libbpfgo := libbpfgoPath()
	for _, path := range []string{
		filepath.Join(libbpfgo, "output"),
		filepath.Join(libbpfgo, "output", "libbpf", "libbpf.a"),
		filepath.Join(libbpfgo, "selftest", "common"),
	} {
		_, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("missing libbpfgo static toolchain path %s\n%s",
				path, libbpfgoSetupHint(libbpfgo))
		}
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
	}
	return nil
}

func libbpfgoSetupHint(libbpfgo string) string {
	return strings.Join([]string{
		fmt.Sprintf("expected a local libbpfgo checkout at %s pinned to %s", libbpfgo, libbpfgoRequiredTag),
		"rebuild the static toolchain with:",
		fmt.Sprintf("  git -C %s checkout %s", libbpfgo, libbpfgoRequiredTag),
		fmt.Sprintf("  git -C %s submodule update --init --recursive", libbpfgo),
		fmt.Sprintf("  make -C %s libbpfgo-static", libbpfgo),
	}, "\n")
}

// ensureVMLINUX dumps vmlinux.h from the build host's BTF once; the CO-RE
// relocations make the object portable regardless of which host's types it
// was compiled against.
func ensureVMLINUX() error {
	if _, err := os.Stat(vmlinuxPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", vmlinuxPath, err)
	}
	output, err := sudoOutput("bpftool", "btf", "dump", "file", "/sys/kernel/btf/vmlinux", "format", "c")
	if err != nil {
		return err
	}
	if err := os.WriteFile(vmlinuxPath, []byte(output), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", vmlinuxPath, err)
	}
	return nil
}

// goEnv is the cgo environment every Go command needs once a package imports
// libbpfgo: headers and the static libbpf from the pinned checkout. GOARCH is
// $IORMESH_TARGET_ARCH (amd64 when unset; BpfBuild, which every target using
// goEnv depends on, rejects other values). Cross-compiling cgo needs the
// arch's C compiler and an explicit CGO_ENABLED, which Go turns off by
// default whenever GOARCH differs from the host's.
func goEnv() map[string]string {
	libbpfgo := libbpfgoPath()
	arch := os.Getenv(containerbuild.TargetArchEnv)
	if arch == "" {
		arch = "amd64"
	}
	env := map[string]string{
		"CGO_CFLAGS": fmt.Sprintf("-I%s -I%s", filepath.Join(libbpfgo, "output"),
			filepath.Join(libbpfgo, "selftest", "common")),
		"CGO_LDFLAGS": fmt.Sprintf("-lelf -lzstd %s",
			filepath.Join(libbpfgo, "output", "libbpf", "libbpf.a")),
		"GOARCH":   arch,
		"GOOS":     "linux",
		"LIBBPFGO": libbpfgo,
	}
	if cc, ok := crossCC[arch]; ok && arch != runtime.GOARCH {
		env["CC"] = cc
		env["CGO_ENABLED"] = "1"
	}
	return env
}

func libbpfgoPath() string {
	if libbpfgo := os.Getenv("LIBBPFGO"); libbpfgo != "" {
		return libbpfgo
	}
	return filepath.Clean(filepath.Join(repoRoot(), defaultLibbpfgoPath))
}

func repoRoot() string {
	root, err := os.Getwd()
	if err != nil {
		return "."
	}
	return root
}

// sudoOutput runs cmd as root: directly when mage already is root, else
// through non-interactive sudo, so a missing sudoers rule fails instead of
// hanging on a password prompt.
func sudoOutput(cmd string, args ...string) (string, error) {
	if os.Geteuid() == 0 {
		return sh.Output(cmd, args...)
	}
	return sh.Output("sudo", append([]string{"-n", cmd}, args...)...)
}

func removeFilesByGlob(pattern string) error {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("glob %s: %w", pattern, err)
	}
	for _, match := range matches {
		if err := removeFilesByPath(match); err != nil {
			return err
		}
	}
	return nil
}

func removeFilesByPath(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

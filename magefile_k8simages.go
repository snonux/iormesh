//go:build mage

// The kind acceptance's images and disk guard (magefile_k8sacceptance.go).

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/magefile/mage/sh"
)

// kindTracerImage builds the tracer image from dist/ and the agent jar
// (scripts/container/tracer-image.Containerfile, the release image),
// pushes it to the local registry and returns it pinned by the registry's
// digest — the form k8s.DefaultImage and `-image` take.
func kindTracerImage() (string, error) {
	if err := diskGuard(); err != nil {
		return "", err
	}
	tag := kindRegistryAt + "/iormesh/tracer:m7"
	if err := sh.RunV("sudo", "-n", "docker", "build", "--platform=linux/amd64",
		"--file=scripts/container/tracer-image.Containerfile", "--tag="+tag, "."); err != nil {
		return "", err
	}
	return pushPinned(tag)
}

// kindWorkloadImage builds the workload's pod image from a context of
// just workload.jar (scripts/container/kind-workload.Containerfile; the
// agent jar is the tracer image's, staged into the JVM's root by the
// tracer, task cf4), pushes it and returns it pinned.
func kindWorkloadImage() (string, error) {
	if err := diskGuard(); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "iormesh-kind-workload")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := sh.Copy(filepath.Join(dir, "workload.jar"), "workload.jar"); err != nil {
		return "", err
	}
	tag := kindRegistryAt + "/iormesh/workload:m7"
	if err := sh.RunV("sudo", "-n", "docker", "build",
		"--file=scripts/container/kind-workload.Containerfile", "--tag="+tag, dir); err != nil {
		return "", err
	}
	return pushPinned(tag)
}

// pushedDigest is the digest line `docker push` ends with.
var pushedDigest = regexp.MustCompile(`digest: (sha256:[0-9a-f]{64})`)

// pushPinned pushes tag and returns <repository>@<digest>, the digest as
// the registry reports it (the manifest list's for a BuildKit build,
// which is what the kubelet then shows as the pod's imageID).
func pushPinned(tag string) (string, error) {
	out, err := sh.Output("sudo", "-n", "docker", "push", tag)
	if err != nil {
		return "", err
	}
	m := pushedDigest.FindStringSubmatch(out)
	if m == nil {
		return "", fmt.Errorf("docker push %s: no digest in %q", tag, out)
	}
	repo := tag[:strings.LastIndex(tag, ":")]
	fmt.Println("pushed", repo+"@"+m[1])
	return repo + "@" + m[1], nil
}

// diskGuard refuses to go on when the repository's filesystem has less
// than minFreeGiB free, or, on btrfs, when metadata is within 0.5 GiB of
// its allocation while less than 2 GiB is unallocated (the filesystem
// could not grow metadata and would go read-only). It never balances.
func diskGuard() error {
	out, err := sh.Output("df", "-B1G", "--output=avail", ".")
	if err != nil {
		return err
	}
	lines := strings.Fields(out)
	free, err := strconv.Atoi(lines[len(lines)-1])
	if err != nil {
		return fmt.Errorf("df: %q: %w", out, err)
	}
	if free < minFreeGiB {
		return fmt.Errorf("disk guard: %d GiB free, need %d", free, minFreeGiB)
	}
	return btrfsGuard()
}

// btrfsGuard is diskGuard's btrfs half; on another filesystem (the btrfs
// commands fail) it passes.
func btrfsGuard() error {
	df, err := sh.Output("sudo", "-n", "btrfs", "filesystem", "df", ".")
	if err != nil {
		return nil
	}
	usage, err := sh.Output("sudo", "-n", "btrfs", "filesystem", "usage", "-g", ".")
	if err != nil {
		return nil
	}
	total, used := gib(df, `Metadata[^:]*: total=([0-9.]+)GiB`), gib(df, `Metadata[^:]*:.*used=([0-9.]+)GiB`)
	if total == 0 {
		return nil // not the format this guard knows: df's check stands
	}
	unalloc := gib(usage, `Device unallocated:\s+([0-9.]+)GiB`)
	if used >= total-0.5 && unalloc < 2 {
		return fmt.Errorf("disk guard: btrfs metadata %.2f of %.2f GiB with %.2f GiB unallocated", used, total, unalloc)
	}
	return nil
}

// gib is the first submatch of pattern in s as a float, 0 without one.
func gib(s, pattern string) float64 {
	m := regexp.MustCompile(pattern).FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	return v
}

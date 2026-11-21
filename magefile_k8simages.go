//go:build mage

// The kind acceptance's images and disk guard (magefile_k8sacceptance.go,
// internal/kindrun).

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/magefile/mage/sh"

	"iormesh/internal/kindrun"
)

// diskGuard runs kindrun.DiskGuard and prints its notice.
func diskGuard(r shRunner) error {
	notice, err := kindrun.DiskGuard(r)
	if notice != "" {
		fmt.Println("NOTICE", notice)
	}
	return err
}

// kindTracerImage builds the tracer image from dist/ and the agent jar
// (scripts/container/tracer-image.Containerfile, the release image),
// labelled, pushes it and returns it pinned by the registry's digest —
// the form k8s.DefaultImage and `-image` take.
func kindTracerImage(r shRunner, c kindrun.Config) (string, error) {
	if err := diskGuard(r); err != nil {
		return "", err
	}
	tag := c.Images[0]
	if err := r.Run(kindrun.BuildArgv(c, "scripts/container/tracer-image.Containerfile", tag, ".")...); err != nil {
		return "", err
	}
	return pushPinned(tag)
}

// kindWorkloadImage builds the workload's pod image from a context of
// just workload.jar (scripts/container/kind-workload.Containerfile; the
// agent jar is the tracer image's, staged into the JVM's root by the
// tracer, task cf4), labelled, pushes it and returns it pinned.
func kindWorkloadImage(r shRunner, c kindrun.Config) (string, error) {
	if err := diskGuard(r); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "iormesh-kind-workload-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := sh.Copy(filepath.Join(dir, "workload.jar"), "workload.jar"); err != nil {
		return "", err
	}
	tag := c.Images[1]
	if err := r.Run(kindrun.BuildArgv(c, "scripts/container/kind-workload.Containerfile", tag, dir)...); err != nil {
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

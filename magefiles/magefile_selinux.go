//go:build mage

// The iormesh-selinux RPM (docs/decisions.md #13, task 7h4): the opt-in
// iormesh_container_t policy module, built on Rocky Linux 9 in a throwaway
// container so the build host needs no SELinux tools. The command line
// lives in internal/containerbuild (SelinuxRPMArgv), where it is
// unit-tested; the policy itself is pinned by packaging/selinux's tests.

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/magefile/mage/sh"

	"iormesh/internal/containerbuild"
)

// SelinuxRPM builds the iormesh-selinux RPM and its source RPM into
// dist/selinux/ with engine (docker or podman), e.g. "mage selinuxRPM
// podman". Install it on an SELinux-enforcing el9 node (it requires
// container-selinux) and deploy with -selinux-type iormesh_container_t
// and the pod's -selinux-level; docs/security.md.
func SelinuxRPM(engine string) error {
	if err := containerbuild.CheckEngine(engine); err != nil {
		return err
	}
	if err := checkEngineUsable(engine); err != nil {
		return err
	}
	src, err := filepath.Abs(containerbuild.SelinuxDir)
	if err != nil {
		return err
	}
	out, err := filepath.Abs(containerbuild.SelinuxDist)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	owner := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	argv := containerbuild.SelinuxRPMArgv(engine, src, out, owner)
	return sh.RunV(argv[0], argv[1:]...)
}

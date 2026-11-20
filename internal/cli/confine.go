package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"iormesh/internal/segment"
)

// The one sudoers rule (PLAN.md M4, decision #1) lets a deploying user
// run the installed binary as root with arguments of their choosing. A
// run's path flags would then be root writes wherever the caller points
// them — -out and -cpuprofile truncate a file, -ready removes and creates
// one, -trace-dir chowns and fills a directory, -spool makes one
// world-writable. So every session started that way is confined: start
// always (whoever calls it, its unit runs as root), and run when the
// installed binary is run through sudo.
const (
	// installedBinary is the path the sudoers rule names.
	installedBinary = "/opt/iormesh/bin/iormesh"
	// defaultSpoolRoot holds the confined sessions' agent spools, one per
	// project: /run is root's, so nothing in the path can be planted.
	defaultSpoolRoot = "/run/iormesh/spool"
)

// confinement is what a confined session may use: segments only under
// traceRoot, a spool only at spoolRoot/<project>, and ready/ only for
// the sudo caller.
type confinement struct {
	traceRoot, spoolRoot string
	// sudoOwner is the sudo caller as UID:GID, "" without sudo (or under
	// root's own sudo).
	sudoOwner string
}

// defaultConfinement is the production one, for env's sudo caller.
func defaultConfinement(env func(string) string) confinement {
	k := confinement{traceRoot: defaultTraceDir, spoolRoot: defaultSpoolRoot}
	if uid := env("SUDO_UID"); uid != "" && uid != "0" {
		k.sudoOwner = uid + ":" + env("SUDO_GID")
	}
	return k
}

// runConfinement decides whether a run is confined: the installed binary
// (the one the sudoers rule names) run through sudo. A build run with
// `sudo ./iormesh run -out DIR` — standalone use, the operator's own
// full sudo — is not; nor is the unit's run, which start confined.
func runConfinement(self string, env func(string) string) (confinement, bool) {
	if env("SUDO_UID") == "" || filepath.Clean(self) != installedBinary {
		return confinement{}, false
	}
	return defaultConfinement(env), true
}

// check refuses what a confined session may not do. Every error wraps
// errUsage.
func (k confinement) check(cfg runConfig) error {
	refuse := func(flag, why string) error {
		return fmt.Errorf("%w: %s is not allowed under start or sudo: %s", errUsage, flag, why)
	}
	switch {
	case cfg.out != "":
		return refuse("-out", "it writes files wherever it points, as root")
	case cfg.cpuProfile != "":
		return refuse("-cpuprofile", "it truncates whatever file it names, as root")
	case cfg.ready != "":
		return refuse("-ready", "it removes and creates whatever file it names, as root")
	case filepath.Clean(cfg.traceDir) != k.traceRoot:
		return refuse("-trace-dir "+cfg.traceDir, "segments go to "+k.traceRoot+" only")
	case cfg.spool != "" && filepath.Clean(cfg.spool) != k.spool(cfg.project):
		return refuse("-spool "+cfg.spool, "the project's spool is "+k.spool(cfg.project))
	case k.sudoOwner != "" && cfg.readyOwner != nil && owner(cfg.readyOwner) != k.sudoOwner:
		return refuse("-ready-owner "+owner(cfg.readyOwner), "ready/ goes to the sudo caller, "+k.sudoOwner)
	}
	return nil
}

// spool is a confined project's spool directory.
func (k confinement) spool(project string) string {
	return filepath.Join(k.spoolRoot, project)
}

// prepare creates the spool root a confined spool lives in (MakeSpool
// creates only the spool itself), 0755 and root's.
func (k confinement) prepare(cfg runConfig) error {
	if cfg.spool == "" {
		return nil
	}
	if err := segment.MakeDirs(k.spoolRoot); err != nil {
		return fmt.Errorf("spool root: %w", err)
	}
	return nil
}

func owner(o *segment.Owner) string {
	return strconv.Itoa(o.UID) + ":" + strconv.Itoa(o.GID)
}

// confineRun applies runConfinement to a parsed run.
func confineRun(cfg runConfig) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("own binary: %w", err)
	}
	k, ok := runConfinement(self, os.Getenv)
	if !ok {
		return nil
	}
	if err := k.check(cfg); err != nil {
		return err
	}
	return k.prepare(cfg)
}

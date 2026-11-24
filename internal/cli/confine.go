package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"iormesh/internal/hostlayout"
	"iormesh/internal/segment"
)

// The one sudoers rule (docs/milestones.md M4, decision #1) lets a deploying
// user run the installed binary as root with arguments of their choosing. A
// run's path flags would then be root writes wherever the caller points
// them — -out and -cpuprofile truncate a file, -ready removes and creates
// one, -trace-dir chowns and fills a directory, -spool makes one
// world-writable. So through the rule only the lifecycle subcommands run
// at all (sudoAllowed; normalize -out, say, would overwrite any file),
// and every session started that way is confined: start always (whoever
// calls it, its unit runs as root), run when it came through the rule
// (sudoGated), and the unit's run of a start again (unitConfinedEnv),
// which gets exactly the values start checked. A confined session's -agent is pinned too: root
// live-loads it into JVMs of every user, so through the rule it may name
// only the jar `iormesh upgrade` verified and installed (task kf4).
const (
	// installedBinary is the path the sudoers rule names: hostlayout's,
	// the one source of the rule bootstrap installs (task qh4).
	installedBinary = hostlayout.Binary
	// defaultSpoolRoot holds the confined sessions' agent spools, one per
	// project: /run is root's, so nothing in the path can be planted.
	defaultSpoolRoot = "/run/iormesh/spool"
	// installedAgent is the agent jar upgrade installs (agentRel).
	installedAgent = installRoot + "/" + agentRel
)

// confinement is what a confined session may use: segments only under
// traceRoot, a spool only at spoolRoot/<project>, the agent jar only at
// agent, and ready/ only for the sudo caller.
type confinement struct {
	traceRoot, spoolRoot, agent string
	// sudoOwner is the sudo caller as UID:GID, "" without sudo (or under
	// root's own sudo).
	sudoOwner string
}

// defaultConfinement is the production one, for env's sudo caller.
func defaultConfinement(env func(string) string) confinement {
	k := confinement{traceRoot: defaultTraceDir, spoolRoot: defaultSpoolRoot, agent: installedAgent}
	if uid := env("SUDO_UID"); uid != "" && uid != "0" {
		k.sudoOwner = uid + ":" + env("SUDO_GID")
	}
	return k
}

// sudoAllowed are the only subcommands the sudoers rule may run (run
// confined): the server lifecycle. Everything else — normalize, attach,
// and any subcommand added later — is refused through the rule, so a new
// one is closed until it is deliberately added here. upgrade is here
// because its only input is a staged file, which it installs only once the
// signature verifies against the root-owned pinned key, names this arch's
// tracer and is not older than the running binary (upgrade.go);
// everything else it touches is fixed.
var sudoAllowed = map[string]bool{"run": true, "start": true, "stop": true, "status": true,
	"upgrade": true, "uninstall": true, "help": true, "-h": true, "--help": true}

// sudoGated reports whether this process came through the sudoers rule:
// SUDO_UID is set and the binary (self, os.Executable's resolved path)
// lies under installRoot. It fails closed: an installed path that is a
// symlink — whose resolved target lies elsewhere and so would not match —
// gates too, as does a binary whose path is unknown ("").
func sudoGated(self string, env func(string) string, lstat func(string) (os.FileInfo, error)) bool {
	if env("SUDO_UID") == "" {
		return false
	}
	if self == "" {
		return true
	}
	if rel, err := filepath.Rel(installRoot, filepath.Clean(self)); err == nil && filepath.IsLocal(rel) {
		return true
	}
	fi, err := lstat(installedBinary)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// checkSudoCommand refuses cmd when the process came through the sudoers
// rule and cmd is not a lifecycle subcommand: normalize, say, would
// create and overwrite files as root wherever -out points.
func checkSudoCommand(cmd, self string, env func(string) string, lstat func(string) (os.FileInfo, error)) error {
	if !sudoGated(self, env, lstat) || sudoAllowed[cmd] {
		return nil
	}
	return fmt.Errorf("%s is not allowed through the sudoers rule (only run, start, stop, status, "+
		"upgrade and uninstall are); run it without sudo or as root directly", cmd)
}

// sudoCommandGate is checkSudoCommand for this process.
func sudoCommandGate(cmd string) error {
	self, err := os.Executable()
	if err != nil {
		self = "" // unknown: gated whenever SUDO_UID is set
	}
	return checkSudoCommand(cmd, self, os.Getenv, os.Lstat)
}

// unitConfinedEnv marks the run of a unit `start` launched (start sets it
// with systemd-run --setenv): start checked the session's flags already,
// and the unit's run checks them again — defence in depth, so a path
// start checked but something changed before the unit ran (task kf4's
// review: a re-pointed -agent symlink) is refused there too. The variable
// only ever adds confinement, so setting it by hand gains nothing.
const unitConfinedEnv = "IORMESH_CONFINED_UNIT"

// runConfinement decides whether a run is confined: it came through the
// sudoers rule (sudoGated), or it is the unit's run of a start
// (unitConfinedEnv). A build run with `sudo bin/iormesh run -out DIR` —
// standalone use, the operator's own full sudo — is not.
func runConfinement(self string, env func(string) string) (confinement, bool) {
	if !sudoGated(self, env, os.Lstat) && env(unitConfinedEnv) != "1" {
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
	case cfg.agent != "" && !k.isAgent(cfg.agent):
		return refuse("-agent "+cfg.agent, "only the installed, signature-checked "+k.agent+" (iormesh upgrade)")
	case k.sudoOwner != "" && cfg.readyOwner != nil && owner(cfg.readyOwner) != k.sudoOwner:
		return refuse("-ready-owner "+owner(cfg.readyOwner), "ready/ goes to the sudo caller, "+k.sudoOwner)
	}
	return nil
}

// isAgent reports whether path, the run's -agent as trustedAgent resolved
// it, is the installed jar: its path, or its real path where /opt is a
// symlink (image-based systems), which is what trustedAgent hands on.
func (k confinement) isAgent(path string) bool {
	path = filepath.Clean(path)
	if path == k.agent {
		return true
	}
	real, err := filepath.EvalSymlinks(k.agent)
	return err == nil && path == real
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

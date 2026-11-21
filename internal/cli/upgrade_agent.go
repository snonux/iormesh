package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"iormesh/internal/release"
	"iormesh/internal/version"
)

// The agent jar's half of `iormesh upgrade` (task kf4, PLAN.md decision
// #28's "the agent jar ships like the tracer"). A JVM traced with
// -javaagent, or live-attached by a session's -agent, runs the jar's code
// as its own user — root's JVMs included — so the jar gets the tracer's
// trust path: signed with the release key, verified here against the
// root-owned pinned key on bytes read once, and installed as a fresh
// root-owned file in a directory nobody else may write. A confined
// session may load no other jar (confinement.agent).

// agentPath is the installed agent jar.
func (u upgrader) agentPath() string { return filepath.Join(u.root, agentRel) }

// lockedInstallAgent installs a verified jar, under the upgrade lock. Its
// signed version must equal the running tracer's, which through the rule
// is the installed one (checkInstalled): deploy upgrades the tracer first
// and the jar with the new binary. The very bytes installed already are a
// no-op; through the rule the same version with other bytes is refused,
// as for the tracer — a rebuild may not stand in for the release.
func (u upgrader) lockedInstallAgent(m release.Meta, data []byte) error {
	if err := u.checkInstalled(); err != nil {
		return fmt.Errorf("upgrade: install layout: %w", err)
	}
	cmp, err := version.Compare(m.Version, u.running)
	if err != nil {
		return fmt.Errorf("upgrade: %w", err)
	}
	if cmp != 0 {
		return fmt.Errorf("upgrade: %s %s does not match the installed tracer %s; upgrade the tracer first",
			m.File, m.Version, u.running)
	}
	if err := u.agentDir(); err != nil {
		return fmt.Errorf("upgrade: install layout: %w", err)
	}
	same, err := u.sameAgent(data)
	if err != nil || same {
		if same {
			_, _ = fmt.Fprintf(u.stdout, "iormesh: %s %s is already installed\n", m.File, m.Version)
		}
		return err
	}
	if err := installFile(u.agentPath(), data, u.owner, u.group, 0o644); err != nil {
		return fmt.Errorf("upgrade: %w", err)
	}
	_, _ = fmt.Fprintf(u.stdout, "iormesh: installed %s %s as %s\n", m.File, m.Version, u.agentPath())
	return nil
}

// agentDir makes sure lib/ exists as a directory the owner owns and
// nobody else may write, creating it (0755, owner:group) when missing.
// An existing lib/ that is anything else — a symlink, another owner's, a
// group-writable one — is refused, never repaired. 0755 is set
// explicitly: root's umask must not make the jar unreadable to the JVM
// users it is for.
func (u upgrader) agentDir() error {
	dir := filepath.Dir(u.agentPath())
	if err := os.Mkdir(dir, 0o700); err == nil {
		if err := os.Chown(dir, u.owner, u.group); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0o755); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrExist) {
		return err
	}
	return u.checkDir(dir, os.Lstat)
}

// sameAgent reports whether data is the installed jar's content. No jar
// yet is false; an installed jar that is not a regular file the owner
// owns, writable by nobody else, is refused; other bytes under the same
// version are refused through the rule (lockedInstallAgent's reasoning).
func (u upgrader) sameAgent(data []byte) (bool, error) {
	installed, err := release.ReadFile(u.agentPath(), release.MaxBinarySize, release.OwnedBy(u.owner))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("upgrade: %w", err)
	}
	if bytes.Equal(installed, data) {
		return true, nil
	}
	if u.gated {
		return false, fmt.Errorf("upgrade: %s %s is installed with other bytes; through the sudoers rule a "+
			"new build must carry a new version", release.AgentFile, u.running)
	}
	return false, nil
}

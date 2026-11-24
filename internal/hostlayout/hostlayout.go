// Package hostlayout is the one source of the server install's fixed
// paths and of its sudoers rule (docs/decisions.md #1): the tracer's
// sudo gate (internal/cli confine.go) and uninstall, the controller's
// deploy, rm and bootstrap (internal/ctl), and the docs' tests all read
// them from here, so the rule bootstrap installs is exactly the one the
// gate expects and uninstall recognises. It is cgo-free and imports
// nothing of the project, so the controller can use it.
package hostlayout

import (
	"regexp"
)

const (
	// Root is the install: the binary, the pinned key, the agent jar and
	// the default trace dir. uninstall removes it.
	Root = "/opt/iormesh"
	// Binary is the installed tracer, the only command the rule names.
	Binary = Root + "/bin/iormesh"
	// Key is the root-owned copy of the pinned release key that
	// `iormesh upgrade` verifies against (decision #28).
	Key = Root + "/etc/minisign.pub"
	// SudoersFile is where bootstrap installs the rule.
	SudoersFile = "/etc/sudoers.d/iormesh"
	// SudoersMarker is the rule file's first line. uninstall removes
	// SudoersFile only when it starts with this line: a rule laid down
	// by configuration management (without it) is left alone.
	SudoersMarker = "# iormesh: installed by iormeshctl bootstrap; iormesh uninstall removes it"
	// RuleSuffix is the rule after the user name.
	RuleSuffix = " ALL=(root) NOPASSWD: " + Binary
)

var (
	// sudoUser is a user name a sudoers rule may carry unquoted: no
	// sudoers special character (`,` `:` `=` `!` `#` `%` `+` spaces,
	// quotes or a backslash), not starting with `-`.
	sudoUser = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,31}$`)
	// sudoAlias is what sudoers reads as an alias name, not a user: an
	// upper-case letter, then upper-case letters, digits and `_` — "ALL"
	// (every user) among them. A rule naming one would grant whoever the
	// alias (or ALL) covers, not one user.
	sudoAlias = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

// ValidUser reports whether user may be named in the rule: exactly one
// user, never ALL or an alias.
func ValidUser(user string) bool { return sudoUser.MatchString(user) && !sudoAlias.MatchString(user) }

// Rule is the one sudoers rule for user: run the installed binary, and
// nothing else, as root without a password.
func Rule(user string) string { return user + RuleSuffix }

// SudoersContent is SudoersFile's whole content for user: the marker,
// then the rule.
func SudoersContent(user string) string {
	return SudoersMarker + "\n" + Rule(user) + "\n"
}

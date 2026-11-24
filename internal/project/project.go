// Package project holds the one rule for project labels (docs/milestones.md
// M4, decision #21), shared by the tracer (`iormesh run -project`) and the
// controller's server registry (`iormeshctl add -project`). It is cgo-free
// so iormeshctl can import it; a copy in each binary would drift, and a
// label one side accepts and the other rejects would only fail on deploy.
package project

import "regexp"

// Rule describes Valid's alphabet for error messages.
const Rule = "1-64 of [A-Za-z0-9._-], not starting with . _ -"

// name keeps labels usable as directory names on servers and controllers
// (traces live under …/traces/<project>/) and as the systemd unit suffix
// (iormesh-<project>), and inert in a shell command line: no quote, space,
// slash or leading dash ever reaches a remote argv.
var name = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Valid reports whether label is a usable project label.
func Valid(label string) bool {
	return name.MatchString(label)
}

// Package version holds the iormesh release version and its ordering. It
// is cgo-free and tiny on purpose: the tracer's upgrade compares it with a
// signed build's version to refuse downgrades (decision #1), and the
// controller and the release signing (internal/release) import it too.
package version

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is this build's release version, MAJOR.MINOR.PATCH. A release
// bumps it before `mage sign`: the signature's trusted comment carries it,
// and an upgrade refuses a build whose signed version is older than the
// running one.
const Version = "0.1.0"

// maxPartDigits bounds each number, so Atoi cannot overflow and a version
// stays the short string a trusted comment is meant to hold.
const maxPartDigits = 6

// Parse splits v into its three numbers. It is strict — no "v" prefix, no
// pre-release suffix, no sign, no leading zeros — so two spellings of one
// version can never compare differently.
func Parse(v string) ([3]int, error) {
	var out [3]int
	parts := strings.Split(v, ".")
	if len(parts) != len(out) {
		return out, fmt.Errorf("version %q: want MAJOR.MINOR.PATCH", v)
	}
	for i, p := range parts {
		if !plainNumber(p) {
			return out, fmt.Errorf("version %q: bad number %q", v, p)
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, fmt.Errorf("version %q: %w", v, err)
		}
		out[i] = n
	}
	return out, nil
}

// Compare returns -1, 0 or +1 as a is older than, equal to or newer than
// b; either failing Parse is an error.
func Compare(a, b string) (int, error) {
	pa, err := Parse(a)
	if err != nil {
		return 0, err
	}
	pb, err := Parse(b)
	if err != nil {
		return 0, err
	}
	for i := range pa {
		switch {
		case pa[i] < pb[i]:
			return -1, nil
		case pa[i] > pb[i]:
			return 1, nil
		}
	}
	return 0, nil
}

// plainNumber reports whether p is 1–maxPartDigits ASCII digits without a
// leading zero (but "0" itself).
func plainNumber(p string) bool {
	if p == "" || len(p) > maxPartDigits || (len(p) > 1 && p[0] == '0') {
		return false
	}
	for _, c := range p {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

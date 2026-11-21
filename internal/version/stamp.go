package version

import (
	"bytes"
	"errors"
	"fmt"
)

// The stamp lets `mage sign` read a built binary's version without
// running it (task uf4): dist/ holds arm64 tracers and darwin
// controllers the signing host cannot execute, and a stale build must
// never be signed as the release. The binary's rodata carries Stamp
// verbatim; Embedded finds it there.
const (
	stampTag = "iormesh-version:"
	// maxStampValue bounds the bytes read after the tag: three numbers of
	// maxPartDigits and two dots.
	maxStampValue = 3*maxPartDigits + 2
)

// Stamp is Version between NULs behind stampTag. What keeps it in a
// binary is the use in Stamped, which the tracer's and the controller's
// version subcommands call: a binary printing Version directly would
// carry only "0.1.0", nothing scannable (the buildgate tests fail then).
// It is a variable so that no compiler folding of the slice in Stamped
// can drop the whole string; with today's Go a constant would be kept
// as well.
var Stamp = "\x00" + stampTag + Version + "\x00"

// ErrNoStamp is a binary without a readable version stamp: not an iormesh
// build, or one from before the stamp existed.
var ErrNoStamp = errors.New("no iormesh version stamp")

// Stamped is the version read back from Stamp (equal to Version). The
// version subcommands print it, which is what keeps Stamp linked in.
func Stamped() string {
	return Stamp[1+len(stampTag) : len(Stamp)-1]
}

// Embedded returns the version stamped into a built binary. Every NUL-
// delimited occurrence of the tag counts whose value parses as a version;
// other occurrences (the tag spelled in this very function, followed by
// whatever string the linker put next) are skipped. No valid stamp is
// ErrNoStamp, two different ones an error: a binary has one version.
func Embedded(data []byte) (string, error) {
	tag := []byte("\x00" + stampTag)
	found := ""
	for rest := data; ; {
		i := bytes.Index(rest, tag)
		if i < 0 {
			break
		}
		rest = rest[i+len(tag):]
		end := bytes.IndexByte(rest[:min(len(rest), maxStampValue+1)], 0)
		if end < 0 {
			continue
		}
		v := string(rest[:end])
		if _, err := Parse(v); err != nil {
			continue
		}
		if found != "" && found != v {
			return "", fmt.Errorf("two version stamps, %s and %s", found, v)
		}
		found = v
	}
	if found == "" {
		return "", ErrNoStamp
	}
	return found, nil
}

package dist

import (
	"fmt"
	"strings"
)

// UntaggedFile marks an untagged build (decision #40, `mage
// releaseUntagged`): it sits next to SHA256SUMS and inside every
// archive, beside the artifacts, so neither the directory nor an
// unpacked archive can pass for the release of its version. Its first
// line names the version, the commit and that it is no release.
const UntaggedFile = "UNTAGGED"

// CheckUntagged gates an untagged build on `git describe --tags --dirty
// --always --match 'v[0-9]*'`: the tree must be clean, so the build is
// exactly one commit, and HEAD must not be the tag v<ver> — that commit
// is cut with `mage release`, and a note calling it untagged would be
// false.
func CheckUntagged(describe, ver string) error {
	d := strings.TrimSpace(describe)
	switch {
	case d == "":
		return fmt.Errorf("untagged build: git describe printed nothing")
	case strings.HasSuffix(d, "-dirty"):
		return fmt.Errorf("untagged build: the tree is dirty (%s); commit or stash first", d)
	case d == "v"+ver:
		return fmt.Errorf("untagged build: HEAD is the release tag v%s; cut it with mage release", ver)
	}
	return nil
}

// UntaggedNote is UntaggedFile's text for commit (git rev-parse HEAD, a
// full lowercase SHA-1) at describe, built from version ver. The
// artifacts keep ver's own stamp — internal/version allows nothing but
// MAJOR.MINOR.PATCH, which signing, deploy and the upgrade order rely on
// — so the note is what says this is not ver's release.
func UntaggedNote(commit, describe, ver string) ([]byte, error) {
	c := strings.TrimSpace(commit)
	if len(c) != 40 || strings.Trim(c, "0123456789abcdef") != "" {
		return nil, fmt.Errorf("untagged build: commit %q is no full SHA-1", c)
	}
	return fmt.Appendf(nil, "iormesh %s untagged build of commit %s: NOT a release\n"+
		"\n"+
		"git describe: %s\n"+
		"Built by mage releaseUntagged for tests such as the M8 agent trial\n"+
		"(decision #40). The artifacts carry the stamp %s of the source they\n"+
		"were built from, but no release v%s was cut from this commit. Never\n"+
		"publish these archives; a release comes from mage release at its tag.\n",
		ver, c, strings.TrimSpace(describe), ver, ver), nil
}

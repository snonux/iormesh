package dist

import (
	"archive/zip"
	"errors"
	"time"
)

// The jars `mage agentBuild` packs (the agent jar shipped in every release
// archive, and the bridge jar nested in it) must rebuild to the same bytes
// from the same commit, or an operator's own `mage release` of a tag could
// not match a published archive byte for byte (task bm4). `jar --create`
// stamps every entry with the build time unless given --date, so the
// magefiles pass JarDate(SourceDate(...)): the same fixed time the
// archives' entries carry. The jar tool sorts a directory's entries by name
// itself (seen with the jar of JDK 17.0.9 and 25.0.5, whatever order the
// file system lists them in), so with the date fixed only the JDK varies:
// the same JDK release gives the same bytes (docs/build.md "Releases").

// JarDate formats t for `jar --create --date`, which wants an ISO-8601
// date-time with an offset and stores it as UTC. The jar tool accepts
// 1980-01-02T00:00:00Z to 2099-12-31T23:59:59Z only, the zip format's
// DOS date range; a commit time is well inside it.
func JarDate(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// JarDated reports whether every entry of the jar at path carries t as
// `jar --date JarDate(t)` stores it: a DOS date-time in UTC, which has
// two-second resolution, so t's odd second is rounded down. A jar built
// at another source date (a newer commit, a stale build) reports false,
// so a build that keeps a fresh jar can rebuild one whose date is off.
// An empty jar reports false: the jar tool always writes the manifest.
func JarDated(path string, t time.Time) (bool, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = r.Close() }()
	if len(r.File) == 0 {
		return false, nil
	}
	// Both sides are rounded down to even seconds: an entry may carry an
	// extended timestamp (one-second resolution) besides the DOS one.
	want := t.Unix() &^ 1
	for _, f := range r.File {
		if f.Modified.IsZero() {
			return false, errors.New(path + ": " + f.Name + ": no modification time")
		}
		if f.Modified.Unix()&^1 != want {
			return false, nil
		}
	}
	return true, nil
}

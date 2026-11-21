package release

import (
	"archive/zip"
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"iormesh/internal/version"
)

// ErrStaleBuild is a dist/ artifact whose own version stamp is missing or
// differs from the version being signed (task uf4): signing it would
// vouch for an old build under the new version, and `iormesh upgrade`
// would install it as that version.
var ErrStaleBuild = errors.New("artifact is not stamped with the version being signed")

const (
	// manifestPath is the jar's manifest, where `mage agentBuild` writes
	// Implementation-Version.
	manifestPath = "META-INF/MANIFEST.MF"
	// maxManifest bounds the manifest read: a few hundred bytes in fact.
	maxManifest = 64 << 10
)

// CheckStamp requires the artifact name, with contents data, to carry
// version ver: a jar in its manifest's Implementation-Version, any other
// file (the tracers, the controllers) as version.Stamp in its rodata.
// Both are read statically, so an arm64 or darwin build is checked on
// any signing host.
func CheckStamp(name string, data []byte, ver string) error {
	var got string
	var err error
	if strings.HasSuffix(name, ".jar") {
		got, err = jarVersion(data)
	} else {
		got, err = version.Embedded(data)
	}
	if err != nil {
		return fmt.Errorf("%w: %s: %v; rebuild it", ErrStaleBuild, name, err)
	}
	if got != ver {
		return fmt.Errorf("%w: %s is stamped %s, signing %s; rebuild it", ErrStaleBuild, name, got, ver)
	}
	return nil
}

// jarVersion reads Implementation-Version from the jar's main manifest
// section (the lines before the first blank one), unfolding the
// manifest's 72-byte continuation lines.
func jarVersion(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("not a jar: %w", err)
	}
	f, err := zr.Open(manifestPath)
	if err != nil {
		return "", fmt.Errorf("no %s: %w", manifestPath, err)
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxManifest+1))
	if err != nil {
		return "", fmt.Errorf("%s: %w", manifestPath, err)
	}
	if len(raw) > maxManifest {
		return "", fmt.Errorf("%s is over %d bytes", manifestPath, maxManifest)
	}
	for _, attr := range mainAttributes(raw) {
		if v, ok := strings.CutPrefix(attr, "Implementation-Version: "); ok {
			return v, nil
		}
	}
	return "", fmt.Errorf("%s has no Implementation-Version", manifestPath)
}

// mainAttributes returns the manifest's main-section attributes, each
// with its continuation lines (those starting with a space) joined on.
func mainAttributes(raw []byte) []string {
	var attrs []string
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		switch {
		case line == "":
			return attrs
		case strings.HasPrefix(line, " ") && len(attrs) > 0:
			attrs[len(attrs)-1] += line[1:]
		default:
			attrs = append(attrs, line)
		}
	}
	return attrs
}

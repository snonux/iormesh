// Package dist packs dist/ into the release archives `mage release`
// publishes (docs/build.md "Releases"): one gzipped tarball per arch holding
// the tracer, the agent jar and that arch's controllers, and a SHA256SUMS
// over the tarballs. The archives carry no signature and no key (decision
// #32): each operator signs an unpacked release with their own key
// (`iormeshctl sign`), the one deploy and bootstrap verify against.
//
// Every artifact's own version stamp is checked against the release
// version on the very bytes packed (release.CheckStamp), so an archive
// never carries a stale build. The archives are reproducible: entries in
// name order, fixed mode, owner and mtime, and a gzip header without name
// or time, so the same dist/ and ModTime give the same bytes.
//
// The package is cgo-free and lives apart from internal/release so the
// tracer and the controller, which import that one, do not link tar and
// gzip writers they never use.
package dist

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"iormesh/internal/release"
	"iormesh/internal/version"
)

// SumsFile lists every archive's SHA-256 in sha256sum's format, so
// `sha256sum -c SHA256SUMS` checks a download for corruption. It vouches
// for nothing else: the operator's own signature does (decision #32).
const SumsFile = "SHA256SUMS"

// Options says what Pack packs and where.
type Options struct {
	// DistDir holds the builds (the container builds' and `mage
	// agentBuild`'s dist/); OutDir is created for the archives and
	// SHA256SUMS.
	DistDir, OutDir string
	// Version is the release version (MAJOR.MINOR.PATCH), the one every
	// artifact must be stamped with; it is part of each archive's name.
	Version string
	// Arches are the release arches, one archive each.
	Arches []string
	// ModTime is every entry's mtime (SourceDate), never the wall clock.
	ModTime time.Time
	// Extra are further files (CHANGELOG.md, LICENSE) packed at their
	// base names.
	Extra []string
}

// Artifacts are the dist/ names packed for arch (release.Artifacts): the
// files `iormeshctl sign` signs once an operator unpacked the archive.
func Artifacts(arch string) []string {
	return release.Artifacts(arch)
}

// ArchiveName is arch's archive at ver, e.g. iormesh-0.1.0-amd64.tar.gz;
// its entries sit under the same name without the suffix.
func ArchiveName(ver, arch string) string {
	return archiveDir(ver, arch) + ".tar.gz"
}

func archiveDir(ver, arch string) string {
	return "iormesh-" + ver + "-" + arch
}

// Pack checks and packs one archive per arch into OutDir, then writes
// SumsFile over them. It returns the archives' paths in arch order, then
// SumsFile's. Nothing is written until every arch's artifacts passed.
func Pack(o Options) ([]string, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	archives := make([][]byte, len(o.Arches))
	for i, arch := range o.Arches {
		files, err := o.collect(arch)
		if err != nil {
			return nil, err
		}
		if archives[i], err = writeArchive(archiveDir(o.Version, arch), files, o.ModTime); err != nil {
			return nil, fmt.Errorf("dist: %s: %w", arch, err)
		}
	}
	if err := os.MkdirAll(o.OutDir, 0o755); err != nil {
		return nil, fmt.Errorf("dist: %w", err)
	}
	var paths []string
	var sums strings.Builder
	for i, arch := range o.Arches {
		name := ArchiveName(o.Version, arch)
		path := filepath.Join(o.OutDir, name)
		if err := os.WriteFile(path, archives[i], 0o644); err != nil {
			return nil, fmt.Errorf("dist: %w", err)
		}
		sum := sha256.Sum256(archives[i])
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
		paths = append(paths, path)
	}
	sumsPath := filepath.Join(o.OutDir, SumsFile)
	if err := os.WriteFile(sumsPath, []byte(sums.String()), 0o644); err != nil {
		return nil, fmt.Errorf("dist: %w", err)
	}
	return append(paths, sumsPath), nil
}

// validate refuses options that would name an archive oddly or stamp the
// wall clock into it.
func (o Options) validate() error {
	if _, err := version.Parse(o.Version); err != nil {
		return fmt.Errorf("dist: %w", err)
	}
	if len(o.Arches) == 0 {
		return errors.New("dist: no arches")
	}
	for _, a := range o.Arches {
		if a == "" || strings.ContainsFunc(a, func(r rune) bool {
			return (r < 'a' || r > 'z') && (r < '0' || r > '9')
		}) {
			return fmt.Errorf("dist: bad arch %q", a)
		}
	}
	if o.ModTime.IsZero() {
		return errors.New("dist: no ModTime (SourceDate)")
	}
	return nil
}

// file is one archive entry below the top directory.
type file struct {
	name string
	data []byte
	mode int64
}

// collect reads arch's artifacts, each stamped with the release version,
// and the extras, sorted by name. A signature an earlier `mage sign` left
// in dist/ is not packed: it would be the builder's, not the operator's.
func (o Options) collect(arch string) ([]file, error) {
	var files []file
	for _, name := range Artifacts(arch) {
		// ReadFile refuses a symlink or non-regular file; the stamp is
		// checked on the very bytes packed.
		data, err := release.ReadFile(filepath.Join(o.DistDir, name), release.MaxBinarySize, nil)
		if err != nil {
			return nil, fmt.Errorf("dist: %w", err)
		}
		if err := release.CheckStamp(name, data, o.Version); err != nil {
			return nil, fmt.Errorf("dist: %w", err)
		}
		mode := int64(0o755)
		if name == release.AgentFile {
			mode = 0o644
		}
		files = append(files, file{name, data, mode})
	}
	for _, path := range o.Extra {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("dist: %w", err)
		}
		files = append(files, file{filepath.Base(path), data, 0o644})
	}
	slices.SortFunc(files, func(a, b file) int { return strings.Compare(a.name, b.name) })
	return files, nil
}

// writeArchive is the gzipped tar of files under top/. Owner root, the
// given mtime and the files' fixed modes make it independent of who built
// dist/ and when; gzip's header stays without name and time.
func writeArchive(top string, files []file, mtime time.Time) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(zw)
	hdr := func(name string, typ byte, mode, size int64) *tar.Header {
		return &tar.Header{
			Name: name, Typeflag: typ, Mode: mode, Size: size,
			Uname: "root", Gname: "root", ModTime: mtime.UTC().Truncate(time.Second),
			Format: tar.FormatPAX,
		}
	}
	if err := tw.WriteHeader(hdr(top+"/", tar.TypeDir, 0o755, 0)); err != nil {
		return nil, err
	}
	for _, f := range files {
		if err := tw.WriteHeader(hdr(top+"/"+f.name, tar.TypeReg, f.mode, int64(len(f.data)))); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f.data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// CheckDescribe requires `git describe --tags --dirty --match 'v[0-9]*'`
// to name exactly the tag v<ver> at a clean HEAD: a release is cut from
// its tagged commit, never from a dirty tree or a commit past the tag,
// and the tag must agree with internal/version, which the signatures
// carry.
func CheckDescribe(describe, ver string) error {
	d := strings.TrimSpace(describe)
	switch {
	case strings.HasSuffix(d, "-dirty"):
		return fmt.Errorf("release: the tree is dirty (%s); commit or stash first", d)
	case d != "v"+ver:
		return fmt.Errorf("release: HEAD is %q, not the tag v%s of internal/version.Version; tag the release commit", d, ver)
	}
	return nil
}

// SourceDate is the archives' mtime: $SOURCE_DATE_EPOCH (the
// reproducible-builds convention) when set, else the release commit's
// time (`git log -1 --format=%ct`). Both are Unix seconds.
func SourceDate(epoch, commitTime string) (time.Time, error) {
	s, what := strings.TrimSpace(epoch), "SOURCE_DATE_EPOCH"
	if s == "" {
		s, what = strings.TrimSpace(commitTime), "commit time"
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}, fmt.Errorf("release: %s %q is not a positive Unix time", what, s)
	}
	return time.Unix(n, 0), nil
}

// Package dist packs a signed dist/ into the release archives `mage release`
// publishes (docs/build.md "Releases"): one gzipped tarball per arch holding
// the tracer, the agent jar and that arch's controllers, each next to its
// minisign signature, plus the release public key, and a SHA256SUMS over the
// tarballs.
//
// Every artifact is verified against its signature before it is packed,
// and the bytes packed are the bytes verified (release.ReadVerified), so an
// archive never carries an unsigned, stale or tampered build. The archives
// are reproducible: entries in name order, fixed mode, owner and mtime,
// and a gzip header without name or time, so the same signed dist/ and
// ModTime give the same bytes.
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

	"aead.dev/minisign"

	"iormesh/internal/release"
	"iormesh/internal/version"
)

const (
	// SumsFile lists every archive's SHA-256 in sha256sum's format, so
	// `sha256sum -c SHA256SUMS` checks a download.
	SumsFile = "SHA256SUMS"
	// KeyFile is the release public key inside each archive. It lets an
	// operator verify offline, but it is no trust anchor by itself: compare
	// it with the key published apart from the archive before pinning it.
	KeyFile = "minisign.pub"
)

// Options says what Pack packs and where.
type Options struct {
	// DistDir holds the signed builds (`mage sign`'s dist/); OutDir is
	// created for the archives and SHA256SUMS.
	DistDir, OutDir string
	// Version is the release version (MAJOR.MINOR.PATCH), the one every
	// artifact must be signed at; it is part of each archive's name.
	Version string
	// Arches are the release arches, one archive each.
	Arches []string
	// PublicKey verifies every artifact and is packed as KeyFile.
	PublicKey minisign.PublicKey
	// ModTime is every entry's mtime (SourceDate), never the wall clock.
	ModTime time.Time
	// Extra are further files (CHANGELOG.md, LICENSE) packed at their
	// base names.
	Extra []string
}

// Artifacts are the dist/ names packed for arch: its static tracer, the
// architecture-neutral agent jar, and the controller for Linux and macOS
// on that arch. Each is packed with its NAME.minisig.
func Artifacts(arch string) []string {
	return []string{
		release.TracerFile(arch),
		release.AgentFile,
		"iormeshctl-linux-" + arch,
		"iormeshctl-darwin-" + arch,
	}
}

// ArchiveName is arch's archive at ver, e.g. iormesh-0.1.0-amd64.tar.gz;
// its entries sit under the same name without the suffix.
func ArchiveName(ver, arch string) string {
	return archiveDir(ver, arch) + ".tar.gz"
}

func archiveDir(ver, arch string) string {
	return "iormesh-" + ver + "-" + arch
}

// Pack verifies and packs one archive per arch into OutDir, then writes
// SumsFile over them. It returns the archives' paths in arch order, then
// SumsFile's. Nothing is written until every arch's artifacts verified.
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

// SignSums signs outDir's SumsFile as file:SHA256SUMS at ver, writing
// SHA256SUMS.minisig, and returns its path. One signature then vouches for
// every archive's hash: `minisign -Vm SHA256SUMS` before `sha256sum -c`.
func SignSums(key minisign.PrivateKey, outDir, ver string, now time.Time) (string, error) {
	path := filepath.Join(outDir, SumsFile)
	data, err := release.ReadFile(path, release.MaxBinarySize, nil)
	if err != nil {
		return "", fmt.Errorf("dist: %w", err)
	}
	sig, err := release.Sign(key, data, release.Meta{File: SumsFile, Version: ver}, now)
	if err != nil {
		return "", fmt.Errorf("dist: %w", err)
	}
	sigPath := path + release.SigSuffix
	if err := os.WriteFile(sigPath, sig, 0o644); err != nil {
		return "", fmt.Errorf("dist: %w", err)
	}
	return sigPath, nil
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

// collect reads arch's verified artifacts and signatures, the key and the
// extras, sorted by name.
func (o Options) collect(arch string) ([]file, error) {
	var files []file
	for _, name := range Artifacts(arch) {
		data, sig, m, err := release.ReadVerified(o.PublicKey, filepath.Join(o.DistDir, name))
		if err != nil {
			return nil, fmt.Errorf("dist: %w", err)
		}
		if m.Version != o.Version {
			return nil, fmt.Errorf("dist: %s is signed at version %s, the release is %s; rebuild and sign dist/",
				name, m.Version, o.Version)
		}
		mode := int64(0o755)
		if name == release.AgentFile {
			mode = 0o644
		}
		files = append(files, file{name, data, mode}, file{name + release.SigSuffix, sig, 0o644})
	}
	key, err := o.PublicKey.MarshalText()
	if err != nil {
		return nil, fmt.Errorf("dist: public key: %w", err)
	}
	files = append(files, file{KeyFile, append(key, '\n'), 0o644})
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

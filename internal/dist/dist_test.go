package dist

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"aead.dev/minisign"

	"iormesh/internal/release"
)

const testVersion = "0.3.0"

var testDate = time.Unix(1_763_800_000, 0)

// fakeDist writes a dist/ of every artifact both arches need, each
// stamped with ver as the builds stamp themselves (release.CheckStamp),
// and no signature: `mage release` packs without a key (decision #32).
// Contents are otherwise fake.
func fakeDist(t *testing.T, ver string) string {
	t.Helper()
	dir := t.TempDir()
	for _, arch := range []string{"amd64", "arm64"} {
		for _, n := range Artifacts(arch) {
			writeStamped(t, dir, n, ver)
		}
	}
	return dir
}

// writeStamped writes dir/name stamped at ver: a jar with the manifest
// `mage agentBuild` writes, any other file with version.Stamp's bytes.
func writeStamped(t *testing.T, dir, name, ver string) {
	t.Helper()
	data := []byte("build of " + name + "\x00iormesh-version:" + ver + "\x00")
	if name == release.AgentFile {
		data = agentJar(t, ver)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
		t.Fatal(err)
	}
}

// agentJar is a jar whose manifest carries Implementation-Version ver.
func agentJar(t *testing.T, ver string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("META-INF/MANIFEST.MF")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("Manifest-Version: 1.0\r\nImplementation-Version: " + ver + "\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func packTo(t *testing.T, distDir string, extra ...string) (string, []string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "release")
	paths, err := Pack(Options{
		DistDir: distDir, OutDir: out, Version: testVersion,
		Arches: []string{"amd64", "arm64"}, ModTime: testDate, Extra: extra,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out, paths
}

func TestPackLayout(t *testing.T) {
	distDir := fakeDist(t, testVersion)
	license := filepath.Join(t.TempDir(), "LICENSE")
	if err := os.WriteFile(license, []byte("licence text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, paths := packTo(t, distDir, license)
	want := []string{
		filepath.Join(out, "iormesh-0.3.0-amd64.tar.gz"),
		filepath.Join(out, "iormesh-0.3.0-arm64.tar.gz"),
		filepath.Join(out, SumsFile),
	}
	if !slices.Equal(paths, want) {
		t.Fatalf("Pack = %v, want %v", paths, want)
	}
	entries := readArchive(t, paths[1])
	top := "iormesh-0.3.0-arm64/"
	wantNames := []string{top}
	for _, n := range []string{
		"LICENSE", "iormesh-agent.jar", "iormesh-linux-arm64",
		"iormeshctl-darwin-arm64", "iormeshctl-linux-arm64",
	} {
		wantNames = append(wantNames, top+n)
	}
	var gotNames []string
	for _, e := range entries {
		gotNames = append(gotNames, e.hdr.Name)
		if e.hdr.Uid != 0 || e.hdr.Gid != 0 || !e.hdr.ModTime.Equal(testDate) {
			t.Errorf("%s: uid %d gid %d mtime %v, want 0 0 %v", e.hdr.Name, e.hdr.Uid, e.hdr.Gid, e.hdr.ModTime, testDate)
		}
	}
	if !slices.Equal(gotNames, wantNames) {
		t.Fatalf("arm64 archive entries\n got %v\nwant %v", gotNames, wantNames)
	}
	checkModes(t, entries)
	checkContents(t, entries, distDir)
	checkSums(t, out, paths[:2])
}

// TestPackNeedsNoKey pins decision #32: packing takes no key, the
// environment's signing variables are not read, and nothing signed or
// key-shaped reaches the output — not even a .minisig an earlier `mage
// sign` left in dist/.
func TestPackNeedsNoKey(t *testing.T) {
	t.Setenv("IORMESH_MINISIGN_KEY", "")
	t.Setenv("IORMESH_MINISIGN_PASSWORD", "")
	distDir := fakeDist(t, testVersion)
	if err := os.WriteFile(filepath.Join(distDir, "iormesh-linux-amd64"+release.SigSuffix), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, paths := packTo(t, distDir)
	files, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f.Name(), release.SigSuffix) {
			t.Errorf("output holds %s", f.Name())
		}
	}
	for _, p := range paths[:len(paths)-1] {
		for _, e := range readArchive(t, p) {
			if strings.HasSuffix(e.hdr.Name, release.SigSuffix) || strings.HasSuffix(e.hdr.Name, "/minisign.pub") {
				t.Errorf("%s holds %s", filepath.Base(p), e.hdr.Name)
			}
		}
	}
}

// TestUnpackedReleaseSigns is the operator's path after a download:
// unpack an archive, sign it with their own key (release.SignRelease,
// what `iormeshctl sign` runs), and every artifact verifies as itself at
// the release version — and not with another key.
func TestUnpackedReleaseSigns(t *testing.T) {
	_, paths := packTo(t, fakeDist(t, testVersion))
	dir := t.TempDir()
	for _, e := range readArchive(t, paths[0]) {
		if e.hdr.Typeflag != tar.TypeReg {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(e.hdr.Name)), e.data, os.FileMode(e.hdr.Mode)); err != nil {
			t.Fatal(err)
		}
	}
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := release.SignRelease(priv, dir, testVersion, testDate)
	if err != nil || len(signed) != len(Artifacts("amd64")) {
		t.Fatalf("SignRelease = %v, %v", signed, err)
	}
	for _, n := range Artifacts("amd64") {
		path := filepath.Join(dir, n)
		if m, err := release.VerifyFile(pub, path); err != nil || m != (release.Meta{File: n, Version: testVersion}) {
			t.Errorf("%s: VerifyFile = %+v, %v", n, m, err)
		}
		if _, err := release.VerifyFile(other, path); !errors.Is(err, release.ErrBadSignature) {
			t.Errorf("%s with another key: %v, want ErrBadSignature", n, err)
		}
	}
}

type entry struct {
	hdr  *tar.Header
	data []byte
}

// readArchive returns path's entries, after checking its gzip header
// carries nothing of the packing host: no mtime, no name, OS unknown
// (255), as Go's writer leaves it.
func readArchive(t *testing.T, path string) []entry {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	if !zr.ModTime.IsZero() || zr.Name != "" || zr.Comment != "" || zr.OS != 255 {
		t.Errorf("%s: gzip header mtime %v name %q comment %q OS %d, want zero, empty, empty, 255",
			path, zr.ModTime, zr.Name, zr.Comment, zr.OS)
	}
	tr := tar.NewReader(zr)
	var out []entry
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, entry{hdr, data})
	}
}

// checkModes wants executables 0755 and every other file 0644, whatever
// the mode in dist/ (the fake LICENSE there is 0600).
func checkModes(t *testing.T, entries []entry) {
	t.Helper()
	for _, e := range entries {
		base := filepath.Base(e.hdr.Name)
		want := int64(0o644)
		if strings.HasSuffix(e.hdr.Name, "/") || strings.HasPrefix(base, "iormesh-linux-") ||
			strings.HasPrefix(base, "iormeshctl-") {
			want = 0o755
		}
		if e.hdr.Mode != want {
			t.Errorf("%s: mode %o, want %o", e.hdr.Name, e.hdr.Mode, want)
		}
	}
}

// checkContents wants every packed artifact byte for byte as in dist/.
func checkContents(t *testing.T, entries []entry, distDir string) {
	t.Helper()
	for _, e := range entries {
		base := filepath.Base(e.hdr.Name)
		if !release.IsArtifact(base) {
			continue
		}
		want, err := os.ReadFile(filepath.Join(distDir, base))
		if err != nil || !bytes.Equal(e.data, want) {
			t.Errorf("%s: packed bytes differ from dist/ (%v)", base, err)
		}
	}
}

// checkSums wants SHA256SUMS in sha256sum's format, one line per archive
// in name order, each hash right.
func checkSums(t *testing.T, out string, archives []string) {
	t.Helper()
	f, err := os.Open(filepath.Join(out, SumsFile))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var lines []string
	for s := bufio.NewScanner(f); s.Scan(); {
		lines = append(lines, s.Text())
	}
	if len(lines) != len(archives) {
		t.Fatalf("%s has %d lines, want %d", SumsFile, len(lines), len(archives))
	}
	for i, a := range archives {
		data, err := os.ReadFile(a)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if want := hex.EncodeToString(sum[:]) + "  " + filepath.Base(a); lines[i] != want {
			t.Errorf("line %d = %q, want %q", i, lines[i], want)
		}
	}
}

// TestPackDeterministic packs the same dist/ twice, the second time after
// changing every input file's mtime, and wants identical bytes: the
// archives depend on the builds' contents and Options.ModTime only.
func TestPackDeterministic(t *testing.T) {
	distDir := fakeDist(t, testVersion)
	out1, paths1 := packTo(t, distDir)
	later := time.Now().Add(time.Hour)
	files, err := os.ReadDir(distDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if err := os.Chtimes(filepath.Join(distDir, f.Name()), later, later); err != nil {
			t.Fatal(err)
		}
	}
	out2, paths2 := packTo(t, distDir)
	if len(paths1) != len(paths2) {
		t.Fatalf("runs gave %d and %d files", len(paths1), len(paths2))
	}
	for i := range paths1 {
		a, errA := os.ReadFile(paths1[i])
		b, errB := os.ReadFile(paths2[i])
		if errA != nil || errB != nil {
			t.Fatal(errA, errB)
		}
		if !bytes.Equal(a, b) {
			t.Errorf("%s differs between %s and %s", filepath.Base(paths1[i]), out1, out2)
		}
	}
	for _, p := range paths1[:len(paths1)-1] {
		_ = readArchive(t, p) // checks the gzip header
	}
}

func TestPackRefuses(t *testing.T) {
	tests := []struct {
		name  string
		spoil func(t *testing.T, dir string)
		want  string
	}{
		{"missing artifact", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "iormeshctl-darwin-arm64")); err != nil {
				t.Fatal(err)
			}
		}, "iormeshctl-darwin-arm64"},
		{"unstamped tracer", func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "iormesh-linux-amd64"), []byte("evil"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "iormesh-linux-amd64"},
		{"stale jar", func(t *testing.T, dir string) {
			writeStamped(t, dir, release.AgentFile, "0.2.9")
		}, "stamped 0.2.9"},
		{"symlinked controller", func(t *testing.T, dir string) {
			p := filepath.Join(dir, "iormeshctl-linux-amd64")
			if err := os.Rename(p, p+".real"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(p+".real", p); err != nil {
				t.Fatal(err)
			}
		}, "symlink"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			distDir := fakeDist(t, testVersion)
			tt.spoil(t, distDir)
			out := filepath.Join(t.TempDir(), "release")
			_, err := Pack(Options{DistDir: distDir, OutDir: out, Version: testVersion,
				Arches: []string{"amd64", "arm64"}, ModTime: testDate})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Pack err = %v, want it to name %q", err, tt.want)
			}
			if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a refused Pack wrote %s (%v)", out, err)
			}
		})
	}
}

// TestPackRefusesOtherVersion stamps dist/ at another version: a stale
// build must not be packed as this release.
func TestPackRefusesOtherVersion(t *testing.T) {
	distDir := fakeDist(t, "0.2.9")
	out := filepath.Join(t.TempDir(), "release")
	_, err := Pack(Options{DistDir: distDir, OutDir: out, Version: testVersion,
		Arches: []string{"amd64"}, ModTime: testDate})
	if !errors.Is(err, release.ErrStaleBuild) || !strings.Contains(err.Error(), "0.2.9") {
		t.Fatalf("Pack err = %v, want a stale build naming 0.2.9", err)
	}
}

func TestPackRefusesBadOptions(t *testing.T) {
	distDir := fakeDist(t, testVersion)
	out := filepath.Join(t.TempDir(), "release")
	for _, o := range []Options{
		{DistDir: distDir, OutDir: out, Version: "v0.3.0", Arches: []string{"amd64"}, ModTime: testDate},
		{DistDir: distDir, OutDir: out, Version: testVersion, ModTime: testDate},
		{DistDir: distDir, OutDir: out, Version: testVersion, Arches: []string{"../x"}, ModTime: testDate},
		{DistDir: distDir, OutDir: out, Version: testVersion, Arches: []string{"amd64"}},
	} {
		if _, err := Pack(o); err == nil {
			t.Errorf("Pack(%+v) succeeded", o)
		}
	}
}

func TestCheckDescribe(t *testing.T) {
	tests := []struct {
		describe string
		ok       bool
	}{
		{"v0.3.0", true},
		{"v0.3.0\n", true},
		{"v0.3.0-dirty", false},
		{"v0.3.0-2-gf912990", false},
		{"v0.2.9", false},
		{"0.3.0", false},
		{"", false},
	}
	for _, tt := range tests {
		err := CheckDescribe(tt.describe, testVersion)
		if (err == nil) != tt.ok {
			t.Errorf("CheckDescribe(%q) = %v, want ok %v", tt.describe, err, tt.ok)
		}
	}
}

func TestSourceDate(t *testing.T) {
	tests := []struct {
		epoch, commit string
		want          int64
		ok            bool
	}{
		{"1700000000", "1600000000", 1_700_000_000, true},
		{"", "1600000000\n", 1_600_000_000, true},
		{"", "", 0, false},
		{"soon", "1600000000", 0, false},
		{"-5", "1600000000", 0, false},
	}
	for _, tt := range tests {
		got, err := SourceDate(tt.epoch, tt.commit)
		if (err == nil) != tt.ok || (tt.ok && got.Unix() != tt.want) {
			t.Errorf("SourceDate(%q, %q) = %v, %v; want %d ok %v", tt.epoch, tt.commit, got, err, tt.want, tt.ok)
		}
	}
}

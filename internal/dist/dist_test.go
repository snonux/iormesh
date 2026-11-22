package dist

import (
	"archive/tar"
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

// fakeDist writes a dist/ of every artifact both arches need, each signed
// with a throwaway key generated per run (no private key is committed),
// as `mage sign` would leave it. Contents are fake: Pack checks
// signatures, not what is signed.
func fakeDist(t *testing.T, ver string) (string, minisign.PublicKey) {
	t.Helper()
	dir, pub, _ := fakeDistKeys(t, ver)
	return dir, pub
}

// fakeDistKeys is fakeDist that also returns the signing key.
func fakeDistKeys(t *testing.T, ver string) (string, minisign.PublicKey, minisign.PrivateKey) {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	names := map[string]bool{}
	for _, arch := range []string{"amd64", "arm64"} {
		for _, n := range Artifacts(arch) {
			names[n] = true
		}
	}
	for n := range names {
		writeSigned(t, priv, dir, n, ver, []byte("build of "+n))
	}
	return dir, pub, priv
}

func writeSigned(t *testing.T, priv minisign.PrivateKey, dir, name, ver string, data []byte) {
	t.Helper()
	sig, err := release.Sign(priv, data, release.Meta{File: name, Version: ver}, testDate)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+release.SigSuffix), sig, 0o644); err != nil {
		t.Fatal(err)
	}
}

func packTo(t *testing.T, distDir string, pub minisign.PublicKey, extra ...string) (string, []string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "release")
	paths, err := Pack(Options{
		DistDir: distDir, OutDir: out, Version: testVersion,
		Arches: []string{"amd64", "arm64"}, PublicKey: pub,
		ModTime: testDate, Extra: extra,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out, paths
}

func TestPackLayout(t *testing.T) {
	distDir, pub := fakeDist(t, testVersion)
	license := filepath.Join(t.TempDir(), "LICENSE")
	if err := os.WriteFile(license, []byte("licence text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, paths := packTo(t, distDir, pub, license)
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
		"LICENSE", "iormesh-agent.jar", "iormesh-agent.jar.minisig",
		"iormesh-linux-arm64", "iormesh-linux-arm64.minisig",
		"iormeshctl-darwin-arm64", "iormeshctl-darwin-arm64.minisig",
		"iormeshctl-linux-arm64", "iormeshctl-linux-arm64.minisig",
		KeyFile,
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
	checkKeyAndSignatures(t, entries, pub)
	checkSums(t, out, paths[:2])
}

type entry struct {
	hdr  *tar.Header
	data []byte
}

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
		if strings.HasSuffix(e.hdr.Name, "/") || strings.HasPrefix(base, "iormesh-linux-") && !strings.HasSuffix(base, release.SigSuffix) ||
			strings.HasPrefix(base, "iormeshctl-") && !strings.HasSuffix(base, release.SigSuffix) {
			want = 0o755
		}
		if e.hdr.Mode != want {
			t.Errorf("%s: mode %o, want %o", e.hdr.Name, e.hdr.Mode, want)
		}
	}
}

// checkKeyAndSignatures verifies every packed artifact against its packed
// signature with the packed public key, as an operator would.
func checkKeyAndSignatures(t *testing.T, entries []entry, pub minisign.PublicKey) {
	t.Helper()
	byName := map[string][]byte{}
	for _, e := range entries {
		byName[filepath.Base(e.hdr.Name)] = e.data
	}
	key, err := release.ParsePublicKey(byName[KeyFile])
	if err != nil || key.ID() != pub.ID() {
		t.Fatalf("packed key: %v (id match %v)", err, err == nil && key.ID() == pub.ID())
	}
	for _, n := range Artifacts("arm64") {
		m, err := release.Verify(key, byName[n], byName[n+release.SigSuffix])
		if err != nil || m.File != n || m.Version != testVersion {
			t.Errorf("%s: Verify = %+v, %v", n, m, err)
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
// archives depend on the signed contents and Options.ModTime only.
func TestPackDeterministic(t *testing.T) {
	distDir, pub := fakeDist(t, testVersion)
	out1, paths1 := packTo(t, distDir, pub)
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
	out2, paths2 := packTo(t, distDir, pub)
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
		{"missing signature", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "iormesh-agent.jar.minisig")); err != nil {
				t.Fatal(err)
			}
		}, "iormesh-agent.jar.minisig"},
		{"tampered tracer", func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "iormesh-linux-amd64"), []byte("evil"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "does not match its signature"},
		{"foreign key", func(t *testing.T, dir string) {
			_, priv, err := minisign.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			writeSigned(t, priv, dir, "iormesh-linux-amd64", testVersion, []byte("foreign"))
		}, "signed by key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			distDir, pub := fakeDist(t, testVersion)
			tt.spoil(t, distDir)
			out := filepath.Join(t.TempDir(), "release")
			_, err := Pack(Options{DistDir: distDir, OutDir: out, Version: testVersion,
				Arches: []string{"amd64", "arm64"}, PublicKey: pub, ModTime: testDate})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Pack err = %v, want it to name %q", err, tt.want)
			}
		})
	}
}

// TestPackRefusesOtherVersion signs dist/ with the right key at another
// version: a stale signed build must not be packed as this release.
func TestPackRefusesOtherVersion(t *testing.T) {
	distDir, pub := fakeDist(t, "0.2.9")
	out := filepath.Join(t.TempDir(), "release")
	_, err := Pack(Options{DistDir: distDir, OutDir: out, Version: testVersion,
		Arches: []string{"amd64"}, PublicKey: pub, ModTime: testDate})
	if err == nil || !strings.Contains(err.Error(), "0.2.9") {
		t.Fatalf("Pack err = %v, want a version mismatch naming 0.2.9", err)
	}
}

func TestPackRefusesBadOptions(t *testing.T) {
	distDir, pub := fakeDist(t, testVersion)
	out := filepath.Join(t.TempDir(), "release")
	for _, o := range []Options{
		{DistDir: distDir, OutDir: out, Version: "v0.3.0", Arches: []string{"amd64"}, PublicKey: pub, ModTime: testDate},
		{DistDir: distDir, OutDir: out, Version: testVersion, PublicKey: pub, ModTime: testDate},
		{DistDir: distDir, OutDir: out, Version: testVersion, Arches: []string{"../x"}, PublicKey: pub, ModTime: testDate},
		{DistDir: distDir, OutDir: out, Version: testVersion, Arches: []string{"amd64"}, PublicKey: pub},
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

// TestSignSums wants SHA256SUMS.minisig to verify as file:SHA256SUMS at
// the release version, and to fail once a hash line is altered.
func TestSignSums(t *testing.T) {
	distDir, pub, priv := fakeDistKeys(t, testVersion)
	out, paths := packTo(t, distDir, pub)
	sums := paths[len(paths)-1]
	sigPath, err := SignSums(priv, out, testVersion, testDate)
	if err != nil {
		t.Fatal(err)
	}
	if sigPath != sums+release.SigSuffix {
		t.Fatalf("SignSums = %s, want %s", sigPath, sums+release.SigSuffix)
	}
	m, err := release.VerifyFile(pub, sums)
	if err != nil || m != (release.Meta{File: SumsFile, Version: testVersion}) {
		t.Fatalf("VerifyFile = %+v, %v", m, err)
	}
	if err := os.WriteFile(sums, []byte("0  iormesh-0.3.0-amd64.tar.gz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := release.VerifyFile(pub, sums); !errors.Is(err, release.ErrBadSignature) {
		t.Fatalf("altered sums: err = %v, want ErrBadSignature", err)
	}
}

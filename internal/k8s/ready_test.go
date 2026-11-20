package k8s

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

// writeFile writes content to dir/name, failing the test on error.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// plantedReady is a ready/ dir with two segments and everything the
// tracer must not send: a symlink to a secret, a FIFO, a subdirectory,
// a dot file and a non-segment.
func plantedReady(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "h-s1-1-0.parquet", "seg0")
	writeFile(t, dir, "h-s1-1-0.methods.parquet", "methods0")
	secret := filepath.Join(t.TempDir(), "secret")
	writeFile(t, filepath.Dir(secret), "secret", "secret")
	if err := os.Symlink(secret, filepath.Join(dir, "link.parquet")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo.parquet"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.parquet"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, ".tmp.parquet", "dot")
	writeFile(t, dir, "notes.txt", "txt")
	return dir
}

// TestWriteReadySkipsPlanted: only the regular segment files go into the
// stream, and Unpack takes exactly those.
func TestWriteReadySkipsPlanted(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteReady(&buf, io.Discard, plantedReady(t)); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	taken, err := Unpack(&buf, out)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"h-s1-1-0.methods.parquet", "h-s1-1-0.parquet"}
	slices.Sort(taken)
	if !slices.Equal(taken, want) {
		t.Errorf("taken = %q, want %q", taken, want)
	}
	assertDir(t, out, map[string]string{"h-s1-1-0.parquet": "seg0", "h-s1-1-0.methods.parquet": "methods0"})
}

// TestWriteReadyMissingDir: nothing published yet is an empty stream.
func TestWriteReadyMissingDir(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteReady(&buf, io.Discard, filepath.Join(t.TempDir(), "ready")); err != nil {
		t.Fatal(err)
	}
	taken, err := Unpack(&buf, t.TempDir())
	if err != nil || len(taken) != 0 {
		t.Errorf("Unpack = %q, %v", taken, err)
	}
}

// assertDir checks dir holds exactly want, each file 0600.
func assertDir(t *testing.T, dir string, want map[string]string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(want) {
		t.Errorf("%s holds %v, want %v", dir, entries, want)
	}
	for name, content := range want {
		p := filepath.Join(dir, name)
		b, err := os.ReadFile(p)
		if err != nil || string(b) != content {
			t.Errorf("%s = %q, %v; want %q", name, b, err, content)
			continue
		}
		if fi, _ := os.Lstat(p); fi.Mode() != 0o600 {
			t.Errorf("%s mode %v, want 0600", name, fi.Mode())
		}
	}
}

// hostileTar builds a stream with the given headers, each with body as
// its content (a header's Size wins over the body for oversize tests).
func hostileTar(t *testing.T, hdrs ...*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range hdrs {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			_, _ = tw.Write(bytes.Repeat([]byte("x"), int(min(h.Size, 4))))
		}
	}
	_ = tw.Flush()
	return buf.Bytes()
}

// TestUnpackRefusesHostile: the stream is untrusted — traversal, a
// directory part, a dot name, links, devices, a non-segment, an oversize
// entry and a duplicate are refused, and nothing lands outside dir.
func TestUnpackRefusesHostile(t *testing.T) {
	reg := func(name string, size int64) *tar.Header {
		return &tar.Header{Typeflag: tar.TypeReg, Name: name, Size: size, Mode: 0o777}
	}
	cases := map[string][]*tar.Header{
		"traversal":    {reg("../evil.parquet", 4)},
		"subdir":       {reg("a/b.parquet", 4)},
		"absolute":     {reg("/tmp/evil.parquet", 4)},
		"dot file":     {reg(".x.parquet", 4)},
		"not parquet":  {reg("x.sh", 4)},
		"symlink":      {{Typeflag: tar.TypeSymlink, Name: "l.parquet", Linkname: "/etc/passwd"}},
		"hardlink":     {{Typeflag: tar.TypeLink, Name: "l.parquet", Linkname: "/etc/passwd"}},
		"char device":  {{Typeflag: tar.TypeChar, Name: "c.parquet"}},
		"directory":    {{Typeflag: tar.TypeDir, Name: "d.parquet"}},
		"oversize":     {reg("big.parquet", MaxSegmentSize+1)},
		"duplicate":    {reg("a.parquet", 4), reg("a.parquet", 4)},
		"option-named": {reg("-rf.parquet", 4)},
	}
	for name, hdrs := range cases {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			out := filepath.Join(parent, "out")
			if err := os.Mkdir(out, 0o700); err != nil {
				t.Fatal(err)
			}
			_, err := Unpack(bytes.NewReader(hostileTar(t, hdrs...)), out)
			if !errors.Is(err, ErrStream) {
				t.Fatalf("err = %v, want ErrStream", err)
			}
			if entries, _ := os.ReadDir(parent); len(entries) != 1 {
				t.Errorf("something landed beside out: %v", entries)
			}
		})
	}
}

// TestUnpackTruncated: a stream that ends inside an entry leaves no
// partial file under the segment's name (and no temporary).
func TestUnpackTruncated(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "s.parquet", Size: 1 << 20})
	_, _ = tw.Write([]byte("short"))
	out := t.TempDir()
	if _, err := Unpack(bytes.NewReader(buf.Bytes()), out); err == nil {
		t.Fatal("truncated stream accepted")
	}
	assertDir(t, out, map[string]string{})
}

// TestFetchMovesAndRemoves: Fetch execs `iormesh ready -project P`,
// unpacks the stream into localDir (0700), then removes exactly the
// files that arrived with a second exec.
func TestFetchMovesAndRemoves(t *testing.T) {
	f := newFake(t)
	var buf bytes.Buffer
	if err := WriteReady(&buf, io.Discard, plantedReady(t)); err != nil {
		t.Fatal(err)
	}
	f.tar(t, buf.Bytes())
	local := filepath.Join(t.TempDir(), "store", "shop", "pod")
	k := newTestKubectl(t, testTarget())
	if err := k.Fetch(context.Background(), "/opt/iormesh/traces/shop/ready", local); err != nil {
		t.Fatal(err)
	}
	assertDir(t, local, map[string]string{"h-s1-1-0.parquet": "seg0", "h-s1-1-0.methods.parquet": "methods0"})
	if fi, _ := os.Stat(local); fi.Mode().Perm() != 0o700 {
		t.Errorf("local dir mode %v", fi.Mode())
	}
	exec := []string{"--context=kind-iormesh", "--namespace=shop", "exec", "checkout-7d9f", "--container=iormesh", "--"}
	want := [][]string{
		slices.Concat(exec, []string{"iormesh", "ready", "-project", "shop"}),
		slices.Concat(exec, []string{"iormesh", "ready", "-project", "shop", "-remove", "--",
			"h-s1-1-0.methods.parquet", "h-s1-1-0.parquet"}),
	}
	got := f.calls(t)
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("calls\n got %q\nwant %q", got, want)
	}
}

// TestFetchPartialStillRemoves: a stream refused after one good entry
// keeps that entry and removes it on the tracer — and only it.
func TestFetchPartialStillRemoves(t *testing.T) {
	f := newFake(t)
	f.tar(t, hostileTar(t,
		&tar.Header{Typeflag: tar.TypeReg, Name: "good.parquet", Size: 4},
		&tar.Header{Typeflag: tar.TypeSymlink, Name: "bad.parquet", Linkname: "/etc/shadow"}))
	local := t.TempDir()
	err := newTestKubectl(t, testTarget()).Fetch(context.Background(), "/opt/iormesh/traces/shop/ready/", local)
	if !errors.Is(err, ErrStream) {
		t.Fatalf("err = %v, want ErrStream", err)
	}
	calls := f.calls(t)
	if len(calls) != 2 || !slices.Equal(calls[1][len(calls[1])-2:], []string{"--", "good.parquet"}) {
		t.Errorf("calls = %q", calls)
	}
}

// TestFetchRefusesRemoteDir: only <TraceRoot>/<project>/ready.
func TestFetchRefusesRemoteDir(t *testing.T) {
	f := newFake(t)
	k := newTestKubectl(t, testTarget())
	for _, d := range []string{"/etc", "/opt/iormesh/traces/shop", "/opt/iormesh/traces/../x/ready",
		"/opt/iormesh/traces/-x/ready", "/opt/iormesh/traces/a/b/ready", "opt/iormesh/traces/shop/ready"} {
		if err := k.Fetch(context.Background(), d, t.TempDir()); !errors.Is(err, ErrTransport) {
			t.Errorf("remote dir %q: err = %v", d, err)
		}
	}
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("kubectl ran: %q", calls)
	}
}

// TestRemoveReady: regular segments go, a missing one is no error, a
// symlink or a non-segment name is refused and its target untouched.
func TestRemoveReady(t *testing.T) {
	dir := plantedReady(t)
	if err := RemoveReady(dir, []string{"h-s1-1-0.parquet", "gone.parquet"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "h-s1-1-0.parquet")); !os.IsNotExist(err) {
		t.Errorf("segment still there: %v", err)
	}
	for _, n := range []string{"link.parquet", "fifo.parquet", "../x.parquet", "notes.txt", ".tmp.parquet"} {
		if err := RemoveReady(dir, []string{n}); !errors.Is(err, ErrTransport) {
			t.Errorf("remove %q: err = %v", n, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "link.parquet")); err != nil {
		t.Errorf("symlink removed: %v", err)
	}
}

// TestFetchKubectlFailsAfterStream: a complete stream followed by
// kubectl failing on its own is ErrUnreachable, not a remote status.
func TestFetchKubectlFailsAfterStream(t *testing.T) {
	f := newFake(t)
	var buf bytes.Buffer
	if err := WriteReady(&buf, io.Discard, plantedReady(t)); err != nil {
		t.Fatal(err)
	}
	f.tar(t, buf.Bytes())
	t.Setenv("FAKE_KUBECTL_EXIT", "1")
	err := newTestKubectl(t, testTarget()).Fetch(context.Background(), "/opt/iormesh/traces/shop/ready", t.TempDir())
	var exit *exec.ExitError
	if !errors.Is(err, ErrUnreachable) || errors.As(err, &exit) {
		t.Fatalf("err = %v, want ErrUnreachable without an ExitError", err)
	}
}

// TestFetchRefusesTrailingData: more than drainLimit bytes after the
// tar's end is not a stream the tracer wrote; kubectl is cancelled.
func TestFetchRefusesTrailingData(t *testing.T) {
	f := newFake(t)
	var buf bytes.Buffer
	if err := WriteReady(&buf, io.Discard, plantedReady(t)); err != nil {
		t.Fatal(err)
	}
	buf.Write(make([]byte, drainLimit+tarBlock))
	f.tar(t, buf.Bytes())
	err := newTestKubectl(t, testTarget()).Fetch(context.Background(), "/opt/iormesh/traces/shop/ready", t.TempDir())
	if !errors.Is(err, ErrStream) {
		t.Fatalf("err = %v, want ErrStream", err)
	}
}

// tarBlock is tar's block size.
const tarBlock = 512

// TestUnpackTotalLimit: a stream past the per-fetch total is refused
// after the entries that fit.
func TestUnpackTotalLimit(t *testing.T) {
	old := fetchLimit
	fetchLimit = 6
	defer func() { fetchLimit = old }()
	stream := hostileTar(t,
		&tar.Header{Typeflag: tar.TypeReg, Name: "a.parquet", Size: 4},
		&tar.Header{Typeflag: tar.TypeReg, Name: "b.parquet", Size: 4})
	taken, err := Unpack(bytes.NewReader(stream), t.TempDir())
	if !errors.Is(err, ErrStream) || !slices.Equal(taken, []string{"a.parquet"}) {
		t.Errorf("Unpack = %q, %v; want a.parquet then ErrStream", taken, err)
	}
}

// TestWriteReadyWarnsOversize: a file over MaxSegmentSize is skipped with
// a note on warn, not silently. A sparse file keeps the test cheap.
func TestWriteReadyWarnsOversize(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "big.parquet"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxSegmentSize + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	var out, warn bytes.Buffer
	if err := WriteReady(&out, &warn, dir); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(warn.Bytes(), []byte("big.parquet")) {
		t.Errorf("warn = %q", warn.String())
	}
	if taken, err := Unpack(&out, t.TempDir()); err != nil || len(taken) != 0 {
		t.Errorf("Unpack = %q, %v", taken, err)
	}
}

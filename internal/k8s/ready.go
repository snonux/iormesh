package k8s

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"syscall"
)

// The fetch stream (decisions #12 and #18): the tracer writes its ready/
// segments as a tar to stdout, the controller unpacks it. Neither side
// needs tar, rsync or a shell in the image, and the tracer's subcommand
// takes a project, never a path, so the stream can only ever carry one
// project's published segments.

// MaxSegmentSize bounds one file in the stream. A segment rotates at
// 64 MB (decision #16); 1 GiB leaves room for a hot session's tables
// without letting a hostile stream fill the controller's disk with one
// entry.
const MaxSegmentSize = 1 << 30

// maxEntries bounds the files one stream may carry: a minute per
// segment and a few tables each is far below it for any collect
// interval.
const maxEntries = 100_000

// segmentName is a file the stream may carry: a non-dot *.parquet name
// without a directory part — what the tracer publishes in ready/, and
// never the unpacker's own dot-prefixed temporaries.
var segmentName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,246}\.parquet$`)

// ErrStream marks a fetch stream the controller refuses.
var ErrStream = errors.New("k8s: fetch stream refused")

// WriteReady is the tracer's `iormesh ready -project P`: it writes the
// segments directly in dir as a tar to w. Each name must be a segment
// name, each file is opened O_NOFOLLOW (and O_NONBLOCK, so a planted FIFO
// cannot hang it) and sent only if fstat shows a regular file; anything
// else in dir is skipped. Exactly the fstat size is sent, so a file that
// changes underneath fails the stream rather than sending a torn copy.
// A dir that does not exist yet (nothing published) is an empty stream,
// not an error: the image has no `test` for collect to ask first.
func WriteReady(w io.Writer, dir string) error {
	root, err := os.OpenRoot(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return tar.NewWriter(w).Close()
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return err
	}
	tw := tar.NewWriter(w)
	for _, e := range entries {
		if !segmentName.MatchString(e.Name()) {
			continue
		}
		if err := writeOne(tw, root, e.Name()); err != nil {
			return fmt.Errorf("%s: %w", e.Name(), err)
		}
	}
	return tw.Close()
}

// writeOne adds one segment to the tar, or skips it when it is not a
// regular file. The lstat skips what is plainly not one (a symlink, which
// os.Root would refuse anyway when it leads out of dir); the O_NOFOLLOW
// open and the fstat after it close the race of a file swapped between
// the two, which then fails the stream instead of being followed.
func writeOne(tw *tar.Writer, root *os.Root, name string) error {
	if li, err := root.Lstat(name); err != nil || !li.Mode().IsRegular() {
		return err
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || fi.Size() > MaxSegmentSize {
		return nil
	}
	hdr := &tar.Header{Typeflag: tar.TypeReg, Name: name, Size: fi.Size(), Mode: 0o600,
		ModTime: fi.ModTime(), Format: tar.FormatPAX}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.CopyN(tw, f, fi.Size())
	return err
}

// RemoveReady is the tracer's `iormesh ready -project P -remove NAME…`:
// it deletes the named segments from dir once the controller has them.
// Each name must be a segment name and a regular file; a name already
// gone is not an error, so a repeated remove is harmless.
func RemoveReady(dir string, names []string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, n := range names {
		if !segmentName.MatchString(n) {
			return fmt.Errorf("%w: %q is not a segment name", ErrTransport, n)
		}
		fi, err := root.Lstat(n)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%w: %s is not a regular file", ErrTransport, n)
		}
		if err := root.Remove(n); err != nil {
			return err
		}
	}
	return nil
}

// Unpack is the controller's side: it writes the stream's segments into
// dir and returns the names it took. The stream comes from a pod and is
// untrusted, so only collect's rules pass (as SSH.Fetch's rsync flags):
// regular-file entries with a segment name — no directory part, no dot
// file, no link, device or directory — each at most MaxSegmentSize. The
// tar's modes, owners and times are ignored: every file is created 0600
// under a dot-prefixed temporary name (which the build skips) and renamed
// into place only once complete, all through an os.Root, so nothing is
// ever written outside dir.
func Unpack(r io.Reader, dir string) ([]string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	tr := tar.NewReader(r)
	var taken []string
	seen := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return taken, nil
		}
		if err != nil {
			return taken, fmt.Errorf("%w: %w", ErrStream, err)
		}
		if err := checkEntry(hdr, seen, len(taken)); err != nil {
			return taken, err
		}
		if err := unpackOne(root, hdr.Name, tr, hdr.Size); err != nil {
			return taken, err
		}
		taken = append(taken, hdr.Name)
	}
}

// checkEntry applies collect's rules to one tar header.
func checkEntry(hdr *tar.Header, seen map[string]bool, n int) error {
	switch {
	case n >= maxEntries:
		return fmt.Errorf("%w: more than %d entries", ErrStream, maxEntries)
	case hdr.Typeflag != tar.TypeReg:
		return fmt.Errorf("%w: %q is not a regular file (type %q)", ErrStream, hdr.Name, hdr.Typeflag)
	case !segmentName.MatchString(hdr.Name):
		return fmt.Errorf("%w: %q is not a segment name", ErrStream, hdr.Name)
	case hdr.Size < 0 || hdr.Size > MaxSegmentSize:
		return fmt.Errorf("%w: %q is %d bytes, over %d", ErrStream, hdr.Name, hdr.Size, MaxSegmentSize)
	case seen[hdr.Name]:
		return fmt.Errorf("%w: %q twice", ErrStream, hdr.Name)
	}
	seen[hdr.Name] = true
	return nil
}

// unpackOne writes size bytes of r to name in root, through a temporary.
// A stale temporary from an interrupted fetch is the controller's own
// and is replaced.
func unpackOne(root *os.Root, name string, r io.Reader, size int64) error {
	tmp := "." + name + ".part"
	if err := root.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.CopyN(f, r, size)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = root.Remove(tmp)
		return fmt.Errorf("%s: %w", name, err)
	}
	return root.Rename(tmp, name)
}

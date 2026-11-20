package segment

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"time"

	parquetgo "github.com/parquet-go/parquet-go"

	"iormesh/internal/schema"
)

// Content values of the iormesh.content footer key: the events file says
// ContentEvents, a table file its table name.
const ContentEvents = "events"

// readBatch is how many rows a reader decodes at once: big enough that
// the per-call overhead vanishes, small enough that the decoded rows
// stay small however many the segment holds. The decoder's own buffers
// (a row group's pages) come on top.
const readBatch = 512

// Bounds on a file the reader opens: a segment on the controller came
// from a server, which may be compromised, so the lengths in it are not
// trusted (task ib4's review). The writer publishes at DefaultMaxBytes
// plus at most one tick's events, so twice that is generous; its footer
// is a few KiB. These bounds stop the footer allocations parquet-go
// makes from declared lengths; page and decompression sizes inside a
// file are not bounded here — the controller decodes each collected
// segment once in a memory-capped child process first (internal/ctl,
// checkSegment) and reads in-process only what passed.
const (
	MaxFileSize   = 2 * DefaultMaxBytes
	MaxFooterSize = 8 << 20
)

// nameRE parses a segment file name from the right (Name, TableName):
// <fqdn>-<session>-<start-ts>-<seq>[.<table>].parquet. The fqdn may hold
// dashes, the session none.
var nameRE = regexp.MustCompile(
	`^(.+)-([^-]+)-([0-9]{8}T[0-9]{6}Z)-([0-9]{6,})(?:\.(methods|threads|classes|stacks))?\.parquet$`)

// FileName is what a segment file's name says (Name, TableName).
type FileName struct {
	FQDN, Session string
	Start         time.Time
	Seq           int
	// Table is the table a table file holds, "" for the events file.
	Table string
}

// Meta is a segment file's footer metadata (Writer.meta): what the name
// says plus the project, the schema version and the exact start.
type Meta struct {
	SchemaVersion int
	Project       string
	FQDN, Session string
	Seq           int
	Start         time.Time
	// Content is ContentEvents or the table name.
	Content string
}

// ParseName parses a segment file name. A name that is not one — a dot
// file (rsync's in-flight temporary), anything else in the store — is an
// error, so a reader never guesses what a stray file holds.
func ParseName(name string) (FileName, error) {
	m := nameRE.FindStringSubmatch(name)
	if m == nil {
		return FileName{}, fmt.Errorf("segment: %q is not a segment file name", name)
	}
	start, err := time.Parse(TimeLayout, m[3])
	if err != nil {
		return FileName{}, fmt.Errorf("segment: %q: %w", name, err)
	}
	seq, err := strconv.Atoi(m[4])
	if err != nil {
		return FileName{}, fmt.Errorf("segment: %q: %w", name, err)
	}
	if err := validName("session", m[2], false); err != nil {
		return FileName{}, err
	}
	return FileName{FQDN: m[1], Session: m[2], Start: start, Seq: seq, Table: m[5]}, nil
}

// Base is the name every file of the segment shares, before any table
// infix and the extension: the events file's name without Ext.
func (n FileName) Base() string { return base(n.FQDN, n.Session, n.Start, n.Seq) }

// ReadEvents streams an events file: every row is decoded into one
// reused Event, checked as strictly as the NDJSON reader checks its
// lines (schema.Event.Validate) and handed to fn, which borrows it for
// the call only (schema.Sink's rule). A footer that is not an events
// file of this schema version, a row that does not validate or a file
// the decoder cannot read is an error naming the file; what fn saw up to
// then stays seen, so a caller that must not act on half a file reads it
// once to check first. A non-nil error from fn stops the read and is
// returned as is.
//
// want, if not nil, vets the footer before any row is decoded (the
// caller's check that the file is the segment its name and place say).
func ReadEvents(ctx context.Context, path string, want func(Meta) error,
	fn func(*schema.Event) error) (Meta, error) {
	return readFile(ctx, path, ContentEvents, want, func(ev *schema.Event) error {
		nilEmptyLists(ev)
		if err := ev.Validate(); err != nil {
			return &rowError{fmt.Errorf("event seq %d: %w", ev.Seq, err)}
		}
		return fn(ev)
	})
}

// nilEmptyLists turns the empty lists parquet-go decodes a null LIST
// column into back into absent ones. docs/schema.md: an absent list and
// an empty one are the same record — but Validate checks presence
// (args.argv only on exec), so the reader hands out the NDJSON reader's
// form, nil.
func nilEmptyLists(ev *schema.Event) {
	if len(ev.Args.Argv) == 0 {
		ev.Args.Argv = nil
	}
	if ev.Java != nil && len(ev.Java.Stack) == 0 {
		ev.Java.Stack = nil
	}
	if ev.HTTP != nil {
		if len(ev.HTTP.ReqHeaders) == 0 {
			ev.HTTP.ReqHeaders = nil
		}
		if len(ev.HTTP.RespHeaders) == 0 {
			ev.HTTP.RespHeaders = nil
		}
	}
	if ev.DNS != nil && len(ev.DNS.Addrs) == 0 {
		ev.DNS.Addrs = nil
	}
}

// ReadTable streams a table file (methods, threads, classes, stacks)
// into fn, which borrows each row for the call; table names the content
// the footer must declare. Rows are not validated here: each table type
// has its own Validate, which the caller applies when it keeps a row.
func ReadTable[T any](ctx context.Context, path, table string, fn func(*T) error) (Meta, error) {
	return readFile(ctx, path, table, nil, fn)
}

// Summary is what CheckEvents learned of an events file.
type Summary struct {
	Meta   Meta   `json:"meta"`
	HostID string `json:"host_id"`
	Events uint64 `json:"events"`
}

// CheckEvents reads a whole events file as ReadEvents does and
// summarizes it: the footer, the one host id its events carry (a file
// with events of two hosts, or with none, is an error — it cannot be
// placed) and the event count. It is the check the controller runs on
// every collected segment before reading it for real.
func CheckEvents(ctx context.Context, path string, want func(Meta) error) (Summary, error) {
	var s Summary
	m, err := ReadEvents(ctx, path, want, func(ev *schema.Event) error {
		switch {
		case s.Events == 0:
			s.HostID = ev.HostID
		case ev.HostID != s.HostID:
			return fmt.Errorf("segment: %s: events of two hosts (%s, %s)", path, s.HostID, ev.HostID)
		}
		s.Events++
		return nil
	})
	if err == nil && s.Events == 0 {
		err = fmt.Errorf("segment: %s: no events", path)
	}
	s.Meta = m
	return s, err
}

// ReadMeta reads only a segment file's footer metadata.
func ReadMeta(path string) (Meta, error) {
	f, err := os.Open(path)
	if err != nil {
		return Meta{}, fmt.Errorf("segment: %w", err)
	}
	defer func() { _ = f.Close() }() // read-only: a close error loses nothing
	pf, err := openParquet(f)
	if err != nil {
		return Meta{}, fmt.Errorf("segment: %s: %w", path, err)
	}
	m, err := metaOf(pf)
	if err != nil {
		return Meta{}, fmt.Errorf("segment: %s: %w", path, err)
	}
	return m, nil
}

// rowError marks a row the reader rejected, as opposed to an error the
// caller's fn returned, so readFile wraps only its own errors.
type rowError struct{ err error }

func (e *rowError) Error() string { return e.err.Error() }
func (e *rowError) Unwrap() error { return e.err }

// readFile opens path, checks its footer declares content and passes
// want (nil: anything), and streams its rows into fn in batches of
// readBatch. The footer is parsed once, here.
func readFile[T any](ctx context.Context, path, content string, want func(Meta) error,
	fn func(*T) error) (Meta, error) {
	f, err := os.Open(path)
	if err != nil {
		return Meta{}, fmt.Errorf("segment: %w", err)
	}
	defer func() { _ = f.Close() }() // read-only: a close error loses nothing
	pf, err := openParquet(f)
	if err != nil {
		return Meta{}, fmt.Errorf("segment: %s: %w", path, err)
	}
	m, err := metaOf(pf)
	if err == nil && m.Content != content {
		err = fmt.Errorf("footer says %q, want %q", m.Content, content)
	}
	if err == nil && want != nil {
		err = want(m)
	}
	if err != nil {
		return Meta{}, fmt.Errorf("segment: %s: %w", path, err)
	}
	err = streamRows(ctx, pf, fn)
	var re *rowError
	if errors.As(err, &re) {
		return m, fmt.Errorf("segment: %s: %w", path, re.err)
	}
	return m, err
}

// openParquet opens f as a parquet file after the bounds checks a file
// from a server needs before any of it is parsed (checkBounds), with
// the page index and bloom filters skipped — the reader never uses
// them, and each is one more length a hostile file could inflate.
// parquet-go panics on some malformed footers (a negative make length
// deep in its column loader); that is an input error like any other.
func openParquet(f *os.File) (pf *parquetgo.File, err error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if err := checkBounds(f, st.Size()); err != nil {
		return nil, err
	}
	defer func() {
		if p := recover(); p != nil {
			pf, err = nil, fmt.Errorf("unreadable footer: %v", p)
		}
	}()
	return parquetgo.OpenFile(f, st.Size(), parquetgo.SkipPageIndex(true),
		parquetgo.SkipBloomFilters(true))
}

// checkBounds refuses a file larger than MaxFileSize and a footer
// longer than MaxFooterSize or than the file itself, before parquet-go
// allocates the length the file's last bytes declare (up to 4 GiB).
func checkBounds(f io.ReaderAt, size int64) error {
	if size > MaxFileSize {
		return fmt.Errorf("%d bytes, over the %d a segment may have", size, int64(MaxFileSize))
	}
	if size < 12 { // magic, footer length, magic
		return fmt.Errorf("%d bytes: not a parquet file", size)
	}
	var tail [8]byte
	if _, err := f.ReadAt(tail[:], size-8); err != nil {
		return err
	}
	if string(tail[4:]) != parquetMagic {
		return errors.New("no parquet magic at the end")
	}
	footer := int64(binary.LittleEndian.Uint32(tail[:4]))
	if footer > MaxFooterSize || footer > size-12 {
		return fmt.Errorf("footer of %d bytes in a %d-byte file (at most %d)", footer, size,
			int64(MaxFooterSize))
	}
	return nil
}

// streamRows decodes pf's rows into a reused batch. parquet-go's reader
// panics on some damaged pages instead of returning an error; a segment
// on the controller came over the network from a server, so damage is
// an input error, never a reason to crash the build — the panic is
// turned into a rowError naming the file.
func streamRows[T any](ctx context.Context, pf *parquetgo.File, fn func(*T) error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = &rowError{fmt.Errorf("unreadable: %v", p)}
		}
	}()
	r := parquetgo.NewGenericReader[T](pf)
	defer func() { _ = r.Close() }() // releases buffers; reading is done
	rows := make([]T, readBatch)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Zeroed per batch: the decoder writes only the columns a row
		// has, and a nil pointer from the previous row must not survive
		// as a set field.
		clear(rows)
		n, rerr := r.Read(rows)
		for i := range n {
			if err := fn(&rows[i]); err != nil {
				return err
			}
		}
		switch {
		case errors.Is(rerr, io.EOF):
			return nil
		case rerr != nil:
			return &rowError{rerr}
		}
	}
}

// metaOf reads the footer metadata Writer.meta wrote and checks the
// schema version: a file of another version would be half-read.
func metaOf(pf *parquetgo.File) (Meta, error) {
	get := func(k string) string { v, _ := pf.Lookup("iormesh." + k); return v }
	var m Meta
	var err error
	if m.SchemaVersion, err = strconv.Atoi(get("schema_version")); err != nil {
		return Meta{}, fmt.Errorf("footer schema_version: %w", err)
	}
	if m.SchemaVersion != schema.Version {
		return Meta{}, fmt.Errorf("footer schema_version %d, want %d", m.SchemaVersion, schema.Version)
	}
	if m.Seq, err = strconv.Atoi(get("seq")); err != nil {
		return Meta{}, fmt.Errorf("footer seq: %w", err)
	}
	if m.Start, err = time.Parse(time.RFC3339Nano, get("start")); err != nil {
		return Meta{}, fmt.Errorf("footer start: %w", err)
	}
	m.Project, m.FQDN, m.Session, m.Content = get("project"), get("fqdn"), get("session"), get("content")
	if m.Project == "" || m.FQDN == "" || m.Session == "" || m.Content == "" {
		return Meta{}, errors.New("footer lacks project, fqdn, session or content")
	}
	return m, nil
}

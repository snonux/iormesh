package ctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"iormesh/internal/federate"
	"iormesh/internal/segment"
)

// Every collected segment came from a server, and a compromised server
// can write any bytes into it. The parquet decoder trusts lengths inside
// the file (page sizes, a zstd frame's content), so a hostile segment
// could make it allocate gigabytes — and running out of memory is fatal
// to a Go process, not an error. segment.MaxFileSize and MaxFooterSize
// stop the footer allocations; for the rest, build decodes each segment
// once in a child process (`iormeshctl __check-segment`) whose address
// space is capped: a hostile file kills the child, which fails that
// segment, never the build. Only segments the child decoded are read in
// the build's own process, and decoding is deterministic, so the same
// file needs the same memory there.

const (
	// checkCommand is the hidden subcommand the child runs.
	checkCommand = "__check-segment"
	// checkAddressSpace caps the child's address space (RLIMIT_AS) on
	// top of what the runtime mapped at start: a segment is at most
	// segment.MaxFileSize (128 MiB), and decoding holds a row group's
	// pages, far below it.
	checkAddressSpace = 1 << 30
	// checkTimeout bounds one segment's check: decoding 128 MiB takes
	// seconds, a decompression bomb spinning must not stall the build.
	checkTimeout = 2 * time.Minute
	// hostPinsFileName pins each host directory to the host identity
	// first built from it (federate.vetHosts), next to the database.
	hostPinsFileName = "hosts.json"
	maxHostPinsSize  = 1 << 20
)

// childChecker returns the federate.Checker that runs each check in a
// child of this executable with the given extra address space.
func childChecker(exe string, space int64) federate.Checker {
	return func(ctx context.Context, path string) (segment.Summary, error) {
		ctx, cancel := context.WithTimeout(ctx, checkTimeout)
		defer cancel()
		var stdout, stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, exe, checkCommand, strconv.FormatInt(space, 10), path)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			return segment.Summary{}, fmt.Errorf("segment: %s: check failed (%v): %s", path, err,
				lastLine(stderr.String()))
		}
		var s segment.Summary
		if err := json.Unmarshal(stdout.Bytes(), &s); err != nil {
			return segment.Summary{}, fmt.Errorf("segment: %s: check: %w", path, err)
		}
		return s, nil
	}
}

// lastLine is the last non-empty line of a child's stderr: the error, or
// the runtime's "fatal error: out of memory".
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// checkSegmentCommand is the child: `__check-segment SPACE PATH` caps
// its own address space at what it has mapped now plus SPACE, decodes
// the file (checkFile) and prints the summary as JSON.
func checkSegmentCommand(args []string, e env) error {
	if len(args) != 2 {
		return fmt.Errorf("%w: %s SPACE PATH", errUsage, checkCommand)
	}
	space, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || space <= 0 {
		return fmt.Errorf("%w: %s: bad SPACE %q", errUsage, checkCommand, args[0])
	}
	if err := capMemory(space); err != nil {
		return err
	}
	s, err := checkFile(e.root(), args[1])
	if err != nil {
		return err
	}
	return json.NewEncoder(e.stdout).Encode(s)
}

// checkFile decodes the file its name says it is: a methods table
// (query's -method reads them, task pb4) or an events file. Any other
// table is refused — nothing reads one yet, so nothing may ask for it
// to be trusted.
func checkFile(ctx context.Context, path string) (segment.Summary, error) {
	name, err := segment.ParseName(filepath.Base(path))
	if err != nil {
		return segment.Summary{}, err
	}
	switch name.Table {
	case "":
		return segment.CheckEvents(ctx, path, nil)
	case segment.TableMethods:
		return segment.CheckMethods(ctx, path, nil)
	}
	return segment.Summary{}, fmt.Errorf("segment: %s: no check for %s tables", path, name.Table)
}

// capMemory limits the process's address space to its current size plus
// space (RLIMIT_AS), and sets the GC's soft limit inside it so the heap
// is collected before the hard limit is hit. Without RLIMIT_AS the
// check still runs, and still isolates a crash from the build.
//
// The current size counts address space the runtime reserved but has
// not used yet, above all the rest of the heap arena (64 MiB on
// linux/amd64): the heap can grow into it without a new mapping, so
// the cap is space plus reserved-but-unused mappings (mostly the heap
// arena's rest). That slack is small next to checkAddressSpace, but it
// makes the cap useless as a test of a small decode under a tiny space
// (task 6p4); TestCheckCap allocates past the slack instead, and
// TestCheckSegmentCapsItself checks the real check sets the cap.
func capMemory(space int64) error {
	debug.SetMemoryLimit(space / 2)
	var cur syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_AS, &cur); err != nil {
		return fmt.Errorf("%s: getrlimit: %w", checkCommand, err)
	}
	limit := uint64(mappedBytes() + space)
	if cur.Max < limit {
		limit = cur.Max
	}
	// macOS may refuse or ignore RLIMIT_AS; the check then runs under
	// the soft limit only, still in its own process.
	_ = syscall.Setrlimit(syscall.RLIMIT_AS, &syscall.Rlimit{Cur: limit, Max: cur.Max})
	return nil
}

// mappedBytes is the process's virtual size from /proc/self/statm on
// Linux; elsewhere a conservative guess for a fresh Go process.
func mappedBytes() int64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err == nil {
		if f := strings.Fields(string(data)); len(f) > 0 {
			if pages, err := strconv.ParseInt(f[0], 10, 64); err == nil {
				return pages * int64(os.Getpagesize())
			}
		}
	}
	return 4 << 30
}

// loadHostPins reads the host pins next to the database; a missing file
// is no pins.
func loadHostPins(path string) (map[string]federate.HostIdentity, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]federate.HostIdentity{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("host pins: %w", err)
	}
	if len(data) > maxHostPinsSize {
		return nil, fmt.Errorf("host pins: %s is over %d bytes", path, maxHostPinsSize)
	}
	pins := map[string]federate.HostIdentity{}
	if err := json.Unmarshal(data, &pins); err != nil {
		return nil, fmt.Errorf("host pins: %s: %w", path, err)
	}
	return pins, nil
}

// pinNewHosts adds the identities of host directories not pinned yet and
// writes the file (0600) when it changed. A pin is never overwritten: a
// host reinstalled under the same registry name needs its entry removed
// from the file by the operator, a deliberate step. The file is read
// again under an exclusive lock (hosts.json.lock beside it), so a build
// and a collect pinning a pod at once (task af4 review N2) neither lose
// the other's new pin nor race on the rename.
func pinNewHosts(path string, seen map[string]federate.HostIdentity) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("host pins: %w", err)
	}
	unlock, err := lockPins(path)
	if err != nil {
		return err
	}
	defer unlock()
	pins, err := loadHostPins(path)
	if err != nil {
		return err
	}
	return addPins(path, pins, seen)
}

// lockPins takes the blocking exclusive flock on path's lock file: the
// holders are short-lived (a read, a merge, a rename).
func lockPins(path string) (func(), error) {
	f, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("host pins: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("host pins: lock: %w", err)
	}
	return func() { _ = f.Close() }, nil
}

// addPins merges the unpinned directories of seen into pins and writes
// them when anything was added.
func addPins(path string, pins, seen map[string]federate.HostIdentity) error {
	changed := false
	for dir, id := range seen {
		if _, ok := pins[dir]; !ok {
			pins[dir], changed = id, true
		}
	}
	if !changed {
		return nil
	}
	data, err := json.MarshalIndent(pins, "", "  ")
	if err != nil {
		return fmt.Errorf("host pins: %w", err)
	}
	if err := writeFileAtomic(path, append(data, '\n')); err != nil {
		return fmt.Errorf("host pins: %w", err)
	}
	return nil
}

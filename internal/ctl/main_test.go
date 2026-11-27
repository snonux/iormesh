package ctl

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"syscall"
	"testing"
)

// allocEnv makes the check child allocate that many bytes right after
// capMemory instead of decoding the segment (TestCheckCap): a heap
// allocation of known size, where a decode's needs depend on the file
// and on when the runtime grows its own metadata.
const allocEnv = "IORMESH_TEST_CHECK_ALLOC"

// rlimitEnv names a file the check child writes, after the real check
// (Main), its RLIMIT_AS soft limit and its virtual size then
// (TestCheckSegmentCapsItself): proof that the decode ran under the cap.
const rlimitEnv = "IORMESH_TEST_CHECK_RLIMIT"

// TestMain lets the test binary stand in for iormeshctl as build's
// segment-check child (check.go): build runs os.Executable(), which in a
// test is this binary, with `__check-segment SPACE PATH`.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == checkCommand {
		if n := os.Getenv(allocEnv); n != "" && len(os.Args) == 4 {
			os.Exit(allocUnderCap(os.Args[2], n))
		}
		code := Main(os.Args[1:], os.Stdout, os.Stderr)
		if p := os.Getenv(rlimitEnv); p != "" {
			if err := writeRlimit(p); err != nil {
				fmt.Fprintln(os.Stderr, err)
				code = 1
			}
		}
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// writeRlimit writes "CUR MAPPED": the RLIMIT_AS soft limit and
// mappedBytes, both in bytes.
func writeRlimit(path string) error {
	var cur syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_AS, &cur); err != nil {
		return err
	}
	return os.WriteFile(path, fmt.Appendf(nil, "%d %d", cur.Cur, mappedBytes()), 0o600)
}

// allocUnderCap caps the address space as the check child does, then
// allocates n bytes and prints an empty summary. Running out of the cap
// is fatal to the child, as it is for a decode.
func allocUnderCap(spaceArg, nArg string) int {
	space, err1 := strconv.ParseInt(spaceArg, 10, 64)
	n, err2 := strconv.Atoi(nArg)
	if err1 != nil || err2 != nil {
		fmt.Fprintln(os.Stderr, "bad SPACE or", allocEnv)
		return 2
	}
	if err := capMemory(space); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	b := make([]byte, n)
	runtime.KeepAlive(b)
	fmt.Println("{}")
	return 0
}

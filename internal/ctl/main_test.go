package ctl

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"testing"
)

// allocEnv makes the check child allocate that many bytes right after
// capMemory instead of decoding the segment (TestCheckCap): a heap
// allocation of known size, where a decode's needs depend on the file
// and on when the runtime grows its own metadata.
const allocEnv = "IORMESH_TEST_CHECK_ALLOC"

// TestMain lets the test binary stand in for iormeshctl as build's
// segment-check child (check.go): build runs os.Executable(), which in a
// test is this binary, with `__check-segment SPACE PATH`.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == checkCommand {
		if n := os.Getenv(allocEnv); n != "" && len(os.Args) == 4 {
			os.Exit(allocUnderCap(os.Args[2], n))
		}
		os.Exit(Main(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
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

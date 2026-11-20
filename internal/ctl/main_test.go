package ctl

import (
	"os"
	"testing"
)

// TestMain lets the test binary stand in for iormeshctl as build's
// segment-check child (check.go): build runs os.Executable(), which in a
// test is this binary, with `__check-segment SPACE PATH`.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == checkCommand {
		os.Exit(Main(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

package integrationtests

// Adapted from ior integrationtests/kernel_support_test.go @ b21afc50328c.
// Changes: the probe takes a tracepoint path ("syscalls/sys_enter_openat",
// "sock/inet_sock_set_state") instead of a syscall name, since M1 needs a
// non-syscall tracepoint too; the io_uring probe and the per-expectation
// helper (expectWhereSupported) are not lifted - no M1 scenario needs them.

import (
	"os"
	"path/filepath"
	"testing"
)

// Kernel-support probes.
//
// A scenario must RUN wherever the kernel supports it and may only be
// skipped where it provably cannot work. The probes ask the running kernel,
// never a version number: distribution kernels backport, so "5.14" says
// nothing about what is available. A skip always names the missing
// tracepoint, so a run on a modern kernel that reports one is a finding.

// tracefsRoots are the places tracefs is mounted. Newer systems mount it on
// /sys/kernel/tracing; older ones only expose it below debugfs.
var tracefsRoots = []string{
	"/sys/kernel/tracing",
	"/sys/kernel/debug/tracing",
}

// m1Tracepoints are what the M1 scenarios cannot do without: the file data
// ops and the TCP connect/accept syscalls, and the socket state tracepoint
// the 4-tuple comes from (PLAN.md M1 "TCP lifecycle from the socket").
// accept(2) itself is left out: the JVM accepts with accept4, and arm64
// kernels have no plain accept.
var m1Tracepoints = []string{
	"syscalls/sys_enter_openat",
	"syscalls/sys_enter_read",
	"syscalls/sys_enter_write",
	"syscalls/sys_enter_close",
	"syscalls/sys_enter_connect",
	"syscalls/sys_enter_accept4",
	"sock/inet_sock_set_state",
}

// tracepointExists reports whether the running kernel exposes the
// tracepoint "category/name" under one of roots. The kernel creates the
// directory for exactly the tracepoints it was built with.
func tracepointExists(roots []string, tracepoint string) bool {
	for _, root := range roots {
		info, err := os.Stat(filepath.Join(root, "events", tracepoint))
		if err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// requireRootForProbe skips exactly like newHarness does. tracefs is
// readable by root only, so an unprivileged probe would mistake
// "permission denied" for "tracepoint missing".
func requireRootForProbe(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("requires root for BPF")
	}
}

// requireTracepoints skips the test unless the running kernel provides
// every named tracepoint.
func requireTracepoints(t *testing.T, tracepoints ...string) {
	t.Helper()
	requireRootForProbe(t)
	for _, tp := range tracepoints {
		if !tracepointExists(tracefsRoots, tp) {
			t.Skipf("kernel does not provide the %s tracepoint", tp)
		}
	}
}

func TestTracepointExists(t *testing.T) {
	root := t.TempDir()
	events := filepath.Join(root, "events")
	if err := os.MkdirAll(filepath.Join(events, "syscalls", "sys_enter_openat"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(events, "sock"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A plain file of the right name is not a tracepoint directory.
	if err := os.WriteFile(filepath.Join(events, "sock", "inet_sock_set_state"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	missingRoot := filepath.Join(root, "not-mounted")

	tests := []struct {
		name       string
		roots      []string
		tracepoint string
		want       bool
	}{
		{"present", []string{root}, "syscalls/sys_enter_openat", true},
		{"absent", []string{root}, "syscalls/sys_enter_statmount", false},
		{"file is not a tracepoint", []string{root}, "sock/inet_sock_set_state", false},
		{"falls back to a later root", []string{missingRoot, root}, "syscalls/sys_enter_openat", true},
		{"no tracefs at all", []string{missingRoot}, "syscalls/sys_enter_openat", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tracepointExists(tt.roots, tt.tracepoint); got != tt.want {
				t.Fatalf("tracepointExists(%q) = %v, want %v", tt.tracepoint, got, tt.want)
			}
		})
	}
}

// TestKernelProbeFindsTheM1Tracepoints guards the probes themselves. If
// tracefs is mounted somewhere tracefsRoots does not list, every probed test
// would skip and the run would look green while asserting nothing. Every
// kernel iormesh supports has the M1 tracepoints, so not finding them means
// the probe is blind, which must fail loudly rather than skip.
func TestKernelProbeFindsTheM1Tracepoints(t *testing.T) {
	requireRootForProbe(t)
	for _, tp := range m1Tracepoints {
		if !tracepointExists(tracefsRoots, tp) {
			t.Errorf("no events/%s below any of %v: tracefs is not where the probes look, "+
				"or the kernel lacks what M1 needs", tp, tracefsRoots)
		}
	}
}

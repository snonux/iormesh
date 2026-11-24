package internal_test

// The arch filter of TestLoaderAttachesEveryFamily (loader_root_test.go,
// task jl4). It lives outside the root test files so its own test runs
// without root in `mage test`: buildgate requires every test of a root
// file to be listed in gatecmd.RootBPFTests.

import (
	"slices"
	"testing"

	ibpf "iormesh/internal/bpf"
	"iormesh/internal/tracepoints"
)

// unexpectedSkips returns the skipped probes that goarch does not excuse:
// all but the enter and exit probes of tracepoints.ArchAbsentSyscalls.
func unexpectedSkips(skipped []ibpf.Probe, goarch string) []ibpf.Probe {
	absent := map[string]bool{}
	for _, name := range tracepoints.ArchAbsentSyscalls(goarch) {
		absent["sys_enter_"+name] = true
		absent["sys_exit_"+name] = true
	}
	var out []ibpf.Probe
	for _, p := range skipped {
		if p.Category != "syscalls" || !absent[p.Event] {
			out = append(out, p)
		}
	}
	return out
}

// TestUnexpectedSkips pins TestLoaderAttachesEveryFamily's filter without
// a kernel: amd64 excuses no skip, arm64 only the probes of a syscall it
// lacks - not its replacement, and not a non-syscall probe of that name.
func TestUnexpectedSkips(t *testing.T) {
	sc := func(event string) ibpf.Probe {
		return ibpf.Probe{Program: "handle_" + event, Category: "syscalls", Event: event}
	}
	skipped := []ibpf.Probe{
		sc("sys_enter_open"), sc("sys_exit_open"), sc("sys_enter_openat"),
		{Program: "handle_sys_enter_poll", Category: "raw", Event: "sys_enter_poll"},
	}
	tests := []struct {
		goarch string
		want   []ibpf.Probe
	}{
		{goarch: "amd64", want: skipped},
		{goarch: "arm64", want: skipped[2:]},
		{goarch: "riscv64", want: skipped},
	}
	for _, tc := range tests {
		got := unexpectedSkips(skipped, tc.goarch)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: unexpectedSkips = %v, want %v", tc.goarch, got, tc.want)
		}
	}
}

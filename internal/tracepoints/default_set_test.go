package tracepoints

import (
	"slices"
	"testing"
)

// TestDefaultSyscallsHaveGeneratedHandlers guards the default set against a
// typo or a kernel without one of its syscalls: a name with no generated
// enter/exit pair would silently attach nothing for it.
func TestDefaultSyscallsHaveGeneratedHandlers(t *testing.T) {
	for _, name := range DefaultSyscalls() {
		for _, tp := range []string{"sys_enter_" + name, "sys_exit_" + name} {
			if !slices.Contains(List, tp) {
				t.Errorf("default syscall %q: no generated tracepoint %s", name, tp)
			}
		}
	}
}

// TestDefaultSelectorAttachesExactlyTheDefaultSet walks every generated
// tracepoint, so a neighbour that a loose match would let in (readv, pread64,
// sendto, openat's _2 sibling via a prefix) fails here as well as a member
// that is missing.
func TestDefaultSelectorAttachesExactlyTheDefaultSet(t *testing.T) {
	sel, err := ParseSelectorWithDimensions("", "", DimensionSelectorConfig{})
	if err != nil {
		t.Fatalf("ParseSelectorWithDimensions: %v", err)
	}
	want := DefaultSyscalls()
	var attached []string
	for _, tp := range List {
		if sel.ShouldAttach(tp) {
			attached = append(attached, tp)
		}
	}
	if got, wantN := len(attached), 2*len(want); got != wantN {
		t.Errorf("default selector attaches %d tracepoints, want %d: %v", got, wantN, attached)
	}
	for _, tp := range attached {
		name, ok := SyscallNameFromTracepoint(tp)
		if !ok || !slices.Contains(want, name) {
			t.Errorf("default selector attaches %s, outside DefaultSyscalls", tp)
		}
	}
}

// TestDefaultSetYieldsToExplicitSelectors pins that the default only applies
// when nothing is asked for: an explicit -trace-syscalls replaces it, and a
// -no-trace-syscalls narrows it.
func TestDefaultSetYieldsToExplicitSelectors(t *testing.T) {
	sel, err := ParseSelectorWithDimensions("", "",
		DimensionSelectorConfig{TraceSyscalls: "nanosleep"})
	if err != nil {
		t.Fatalf("ParseSelectorWithDimensions: %v", err)
	}
	if sel.ShouldAttach("sys_enter_openat") || !sel.ShouldAttach("sys_enter_nanosleep") {
		t.Error("an explicit -trace-syscalls must replace the default set")
	}
	sel, err = ParseSelectorWithDimensions("", "",
		DimensionSelectorConfig{NoTraceSyscalls: "connect"})
	if err != nil {
		t.Fatalf("ParseSelectorWithDimensions: %v", err)
	}
	if sel.ShouldAttach("sys_enter_connect") || !sel.ShouldAttach("sys_enter_accept4") {
		t.Error("-no-trace-syscalls must narrow the default set, not replace it")
	}
}

// TestDefaultSyscallsReturnsAFreshSlice: callers may append or sort the
// result without changing the default for everybody else.
func TestDefaultSyscallsReturnsAFreshSlice(t *testing.T) {
	first := DefaultSyscalls()
	first[0] = "mutated"
	if DefaultSyscalls()[0] == "mutated" {
		t.Error("DefaultSyscalls shares its backing array between calls")
	}
}

// TestUnknownSyscallSelectorIsRejected is the negative case: a misspelt
// syscall must fail parsing, not attach nothing silently.
func TestUnknownSyscallSelectorIsRejected(t *testing.T) {
	_, err := ParseSelectorWithDimensions("", "",
		DimensionSelectorConfig{TraceSyscalls: "opnat"})
	if err == nil {
		t.Fatal("ParseSelectorWithDimensions accepted the unknown syscall \"opnat\"")
	}
}

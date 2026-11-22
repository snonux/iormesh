package tracepoints

import (
	"slices"
	"testing"

	"iormesh/internal/schema"
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

// TestDefaultSyscallsAreSchemaKinds: the correlator names an event after
// its syscall, so a default syscall without a schema kind would produce
// events the sink rejects — every attached record lost at the last step.
func TestDefaultSyscallsAreSchemaKinds(t *testing.T) {
	for _, name := range DefaultSyscalls() {
		if !schema.KnownKind(name) {
			t.Errorf("default syscall %q is not a schema kind (schema/kinds_file.go)", name)
		}
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

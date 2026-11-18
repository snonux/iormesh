package tracepoints

import (
	"slices"
	"testing"

	"iormesh/internal/schema"
)

// TestFamilySyscallsAreAttachableKinds: every syscall a family can attach
// has a generated enter/exit pair and a schema kind, so no -trace-families
// selection attaches nothing for a name or writes events the sink rejects.
func TestFamilySyscallsAreAttachableKinds(t *testing.T) {
	for _, name := range SyscallsFor(AllFamilies()) {
		for _, tp := range []string{"sys_enter_" + name, "sys_exit_" + name} {
			if !slices.Contains(List, tp) {
				t.Errorf("family syscall %q: no generated tracepoint %s", name, tp)
			}
		}
		if !schema.KnownKind(name) {
			t.Errorf("family syscall %q is not a schema kind", name)
		}
	}
}

func TestParseTraceFamilies(t *testing.T) {
	tests := []struct {
		raw     string
		want    []TraceFamily
		wantErr bool
	}{
		{raw: "", want: []TraceFamily{FamilyFile, FamilySocket}},
		{raw: " , ", want: []TraceFamily{FamilyFile, FamilySocket}},
		{raw: "signal,ipc", want: []TraceFamily{FamilyFile, FamilySocket, FamilyIPC, FamilySignal}},
		{raw: "IPC, ipc", want: []TraceFamily{FamilyFile, FamilySocket, FamilyIPC}},
		{raw: "file", want: []TraceFamily{FamilyFile, FamilySocket}},
		{raw: "poll,all", want: AllFamilies()},
		{raw: "signals", wantErr: true},
		{raw: "FS", wantErr: true}, // ior's family names are not iormesh's
	}
	for _, tt := range tests {
		got, err := ParseTraceFamilies(tt.raw)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseTraceFamilies(%q) error %v, want error %v", tt.raw, err, tt.wantErr)
			continue
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("ParseTraceFamilies(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}

// TestDefaultSetIsFileAndSocket: the defaults are decision #7's, and the
// opt-in families' syscalls stay out of them.
func TestDefaultSetIsFileAndSocket(t *testing.T) {
	def := DefaultSyscalls()
	for _, sc := range []string{"openat", "statx", "fcntl", "connect", "bind", "sendto", "recvfrom"} {
		if !slices.Contains(def, sc) {
			t.Errorf("default set lacks %s", sc)
		}
	}
	for _, f := range []TraceFamily{FamilyIPC, FamilySignal, FamilyPoll, FamilyZeroCopy} {
		for _, sc := range familySyscalls[f] {
			if slices.Contains(def, sc) {
				t.Errorf("opt-in %s syscall %s is in the default set", f, sc)
			}
		}
	}
}

// TestSyscallsForDeduplicates: read/write/close are in two families and
// appear once.
func TestSyscallsForDeduplicates(t *testing.T) {
	got := SyscallsFor([]TraceFamily{FamilyFile, FamilySocket})
	seen := map[string]bool{}
	for _, sc := range got {
		if seen[sc] {
			t.Errorf("%s listed twice", sc)
		}
		seen[sc] = true
	}
}

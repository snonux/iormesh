package tracepoints

import (
	"fmt"
	"slices"
	"strings"

	"iormesh/internal/csvlist"
)

// TraceFamily is one of iormesh's event families (PLAN.md decision #7):
// the unit `iormesh run -trace-families` turns on. It follows ior's
// trace-family concept (-trace-families), but over iormesh's own grouping:
// ior's families (types.SyscallFamily) put sendfile and splice under
// Network and pipes under IPC, while a family here is what the correlator
// models together — and every syscall in one has a schema kind and a
// correlator shape, so no selection can attach a syscall the trace could
// not write.
type TraceFamily string

// The families. file and socket are on by default, like lifecycle — the
// fork, exec and exit probes, which are not selectable at all because they
// decide what is traced (bpf.ControlProbes). The rest are opt-in: S3 found
// the consumer's per-event cost, not the probes, limits throughput
// (decision #25), so every family that can fire at syscall frequency waits
// to be asked for (decision #7).
const (
	// FamilyFile is M1's open/read/write/close and M2's file surface and
	// fd lifecycle (steps 4–6).
	FamilyFile TraceFamily = "file"
	// FamilySocket is the TCP connect/accept of M1 plus bind and the
	// datagram calls sendto/recvfrom (M2 step 7): unix sockets and UDP
	// are sockets, and their addresses ride on these calls. The msghdr
	// data calls sendmsg/recvmsg/sendmmsg/recvmmsg joined in task oh4.
	FamilySocket TraceFamily = "socket"
	// FamilyIPC is pipes and socketpairs: both ends at once, one
	// connection node.
	FamilyIPC TraceFamily = "ipc"
	// FamilySignal is kill/tgkill/tkill and signal:signal_generate, the
	// delivery with its sender.
	FamilySignal TraceFamily = "signal"
	// FamilyPoll is epoll and poll: registrations and the waits, whose
	// latency is the time a thread blocked.
	FamilyPoll TraceFamily = "poll"
	// FamilyZeroCopy is sendfile, splice, tee and copy_file_range: data
	// that moves without a read or write.
	FamilyZeroCopy TraceFamily = "zerocopy"
)

// allFamilies is every family in documentation order; "all" selects them.
var allFamilies = []TraceFamily{
	FamilyFile, FamilySocket, FamilyIPC, FamilySignal, FamilyPoll, FamilyZeroCopy,
}

// familySyscalls lists each family's syscalls (bare tracepoint names).
// read, write and close sit in file and in socket: they are the I/O of
// every fd, so either family alone still sees a connection's data.
var familySyscalls = map[TraceFamily][]string{
	FamilyFile:   append(m1FileSyscalls(), fileSurfaceSyscalls()...),
	FamilySocket: socketSyscalls(),
	FamilyIPC:    {"pipe", "pipe2", "socketpair"},
	FamilySignal: {"kill", "tgkill", "tkill"},
	FamilyPoll: {"epoll_create", "epoll_create1", "epoll_ctl", "epoll_wait",
		"epoll_pwait", "epoll_pwait2", "poll", "ppoll"},
	FamilyZeroCopy: {"sendfile64", "splice", "tee", "copy_file_range"},
}

// DefaultFamilies returns the families attached without -trace-families.
func DefaultFamilies() []TraceFamily {
	return []TraceFamily{FamilyFile, FamilySocket}
}

// AllFamilies returns every family, defaults first.
func AllFamilies() []TraceFamily {
	return slices.Clone(allFamilies)
}

// ParseTraceFamilies parses -trace-families: a comma-separated list of
// family names, or "all". The result always holds the default families
// first — the flag adds opt-in families, it cannot drop file or socket,
// since the scenario markers are opens and the loopback join needs the
// socket calls — then the named ones in documentation order, each once.
// An empty value is the default set.
func ParseTraceFamilies(raw string) ([]TraceFamily, error) {
	want := map[TraceFamily]bool{}
	all := false
	for _, name := range csvlist.Split(raw) {
		name = strings.ToLower(name)
		if name == "all" {
			all = true // after the loop: "all,bogus" still names a bogus one
			continue
		}
		f := TraceFamily(name)
		if _, ok := familySyscalls[f]; !ok {
			return nil, fmt.Errorf("unknown trace family %q (want %s or all)", name, familyNames())
		}
		want[f] = true
	}
	if all {
		return AllFamilies(), nil
	}
	for _, f := range DefaultFamilies() {
		want[f] = true
	}
	var out []TraceFamily
	for _, f := range allFamilies {
		if want[f] {
			out = append(out, f)
		}
	}
	return out, nil
}

// SyscallsFor returns the syscalls of the given families, each once, in
// family order. The slice is fresh.
func SyscallsFor(families []TraceFamily) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range families {
		for _, sc := range familySyscalls[f] {
			if !seen[sc] {
				seen[sc] = true
				out = append(out, sc)
			}
		}
	}
	return out
}

// familyNames lists the family names for an error message.
func familyNames() string {
	names := make([]string, len(allFamilies))
	for i, f := range allFamilies {
		names[i] = string(f)
	}
	return strings.Join(names, ", ")
}

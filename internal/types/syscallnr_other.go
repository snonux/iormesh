// Adapted from ior internal/types/syscallnr_other.go @ b21afc50328c.
// Changes: none.

//go:build !amd64

package types

// syscallNumbers is empty off x86_64: the table of syscallnr_amd64.go holds
// x86_64 numbers, and the other architectures number their syscalls
// differently. TraceId.SyscallNumber knows no number then.
var syscallNumbers = map[string]int64{}

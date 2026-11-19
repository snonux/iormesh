//go:build !amd64 && !arm64

package bpf

// syscallNumbers is empty where no table is generated
// (magefile_sysnum.go): every syscall probe is then reported missing,
// like a tracepoint the kernel lacks.
var syscallNumbers = map[string]uint32{}

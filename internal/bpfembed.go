// Adapted from ior internal/bpfembed.go @ b21afc50328c.
// Changes: env override renamed to IORMESH_BPF_OBJECT, object to
// iormesh.bpf.o; nothing calls loadBPFModule until the M1 loader (step 5).

package internal

import (
	_ "embed"
	"os"

	bpf "github.com/aquasecurity/libbpfgo"
)

const (
	// bpfObjectOverrideEnv names an external .bpf.o to load instead of the
	// embedded one, so a test can run a modified object without a rebuild.
	bpfObjectOverrideEnv  = "IORMESH_BPF_OBJECT"
	embeddedBPFObjectName = "iormesh.bpf.o"
)

// embeddedBPFObject is the CO-RE object `mage bpfBuild` compiles; embedding
// it keeps the tracer a single static binary that runs on any BTF host.
//
//go:embed c/iormesh.bpf.o
var embeddedBPFObject []byte

// The constructors are variables so tests can substitute them without a
// kernel.
var (
	newBPFModuleFromFile   = bpf.NewModuleFromFile
	newBPFModuleFromBuffer = bpf.NewModuleFromBuffer
)

// loadBPFModule opens the BPF module from the override file when
// IORMESH_BPF_OBJECT is set, else from the embedded object. The returned
// string names the step for error messages.
func loadBPFModule() (*bpf.Module, string, error) {
	if path := os.Getenv(bpfObjectOverrideEnv); path != "" {
		module, err := newBPFModuleFromFile(path)
		return module, "load module from override file", err
	}

	module, err := newBPFModuleFromBuffer(embeddedBPFObject, embeddedBPFObjectName)
	return module, "load embedded module", err
}

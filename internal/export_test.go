package internal

// EmbeddedBPFObject hands the external tests of ./internal the object
// LoadBPFModule embeds, so a root test can open it with an argument the
// production loader never passes - a custom kernel BTF for CO-RE
// (btf_root_test.go, task of4).
func EmbeddedBPFObject() []byte {
	return embeddedBPFObject
}

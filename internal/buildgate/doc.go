// Adapted from ior internal/buildgate/doc.go @ b21afc50328c.
// Changes: describes the trimmed iormesh gate set, plus the cgo-free
// controller gate, the test-only import gate, the C-comment reference gate,
// the controller's terminal-safe print gate and the builder clang pin.

// Package buildgate holds fitness tests for the repository's static-analysis
// gates: that the vet, lint and test command lines stay the reviewed ones,
// that the lint configuration has no unreviewed key, that the lint gate
// really rejects a planted defect, that no lint-suppression directive
// silences it, and that the controller iormeshctl stays cgo-free and
// cross-compiles for macOS (cgofree_test.go), and that test-only packages
// such as internal/corereloc are imported by tests alone (testonly_test.go),
// and that the comments of the BPF C under internal/c name only internal/...
// paths and Test* functions that exist here (ccomments_test.go), and
// that every fmt print in internal/ctl escapes its text values for the
// terminal (ctlprint_test.go), and that each tracer builder pins the clang
// whose BPF object was last loaded on el9 (clangpin_test.go), and that the
// iormesh agent skill under docs/skills/iormesh names only subcommands the
// programs' usage lists, links only to files and headings that exist and
// keeps a frontmatter both Claude Code and pi load (skilldoc_test.go).
//
// It has no production code. The gates it guards are configuration
// (Magefile.go, .golangci.yml, internal/gatecmd) and docs rather than Go
// APIs, so nothing else in the tree fails when they are quietly weakened.
package buildgate

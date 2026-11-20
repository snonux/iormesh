// Adapted from ior internal/buildgate/doc.go @ b21afc50328c.
// Changes: describes the trimmed iormesh gate set, plus the cgo-free
// controller gate.

// Package buildgate holds fitness tests for the repository's static-analysis
// gates: that the vet, lint and test command lines stay the reviewed ones,
// that the lint configuration has no unreviewed key, that the lint gate
// really rejects a planted defect, that no lint-suppression directive
// silences it, and that the controller iormeshctl stays cgo-free and
// cross-compiles for macOS (cgofree_test.go).
//
// It has no production code. The gates it guards are configuration
// (Magefile.go, .golangci.yml, internal/gatecmd) rather than Go APIs, so
// nothing else in the tree fails when they are quietly weakened.
package buildgate

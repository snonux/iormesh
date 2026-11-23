// Package integrationtests holds the end-to-end tests of iormesh: they run
// the real tracer binary (../bin/iormesh) against the real Java workload
// (../bin/workload.jar), read the trace it wrote and golden-check it against
// the scenario's expect.json - every layer once, BPF to golden (PLAN.md
// "M1 - Walking skeleton", "Integration tests - follow ior").
//
// The harness is lifted from ior's integrationtests package
// (../ior @ b21afc50328c); each file names what it took and what changed.
// The tests need root for BPF and skip without it; `mage integrationTest`
// runs them as root and fails unless every one of them reported PASS.
// See docs/testing.md.
package integrationtests

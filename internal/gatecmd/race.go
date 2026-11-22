package gatecmd

import "slices"

// The race gate (`mage testRace`, task ij4): the suite once more under the
// race detector, over every package of the module except RaceExcluded.
// docs/testing.md ("Race detector gate") records the measured costs behind
// the choice.

// RaceExcluded are the packages the race gate leaves out, by import path.
// Each costs far more under -race than it could find:
//
//   - iormesh/internal/generate took 2099 s under -race (2026-11-22). Its
//     tests compile and run many small C harness programs and its Go code
//     starts no goroutine, so the detector has nothing to watch there.
//   - iormesh/internal/ctl took 947 s under -race. Neither its code nor
//     its tests start a goroutine or call t.Parallel; the packages it
//     drives (segment, federate, k8s) are race-checked by their own tests.
//
// Every package that starts goroutines itself (acceptance, bpf, cli,
// stacks) stays in, which is what -race is for. A deny-list rather than a
// list of included packages, so a new package is race-checked from its
// first commit; one that turns out slow is added here with its measured
// cost, and docs/testing.md updated.
func RaceExcluded() []string {
	return []string{"iormesh/internal/ctl", "iormesh/internal/generate"}
}

// RacePackagesEnv, when set to a space-separated package list, replaces the
// race gate's package set - for running one or two packages under -race
// while working on them; the gate itself runs with it unset.
const RacePackagesEnv = "IORMESH_RACE_PKGS"

// RacePackages drops RaceExcluded from listed, the import paths
// `go list ./...` printed, keeping their order.
func RacePackages(listed []string) []string {
	excluded := RaceExcluded()
	var out []string
	for _, p := range listed {
		if !slices.Contains(excluded, p) {
			out = append(out, p)
		}
	}
	return out
}

// ListPackages prints the module's import paths, one per line, for
// RacePackages.
func ListPackages() []string {
	return []string{"go", "list", "./..."}
}

// TestRace is the race gate's command line for pkgs. -count=1 forces a real
// run without dropping the cache `mage test` keeps. The timeout is per test
// binary: every package left in took under a minute under -race on a loaded
// host, so 15 minutes is generous and still stops a hung test well before
// the 90 of TestAll.
func TestRace(pkgs []string) []string {
	return append([]string{"go", "test", "-race", "-count=1", "-failfast", "-timeout=15m"}, pkgs...)
}

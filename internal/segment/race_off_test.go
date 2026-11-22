//go:build !race

package segment

// raceEnabled is false without -race, so TestReadHostileFile runs in every
// ordinary `mage test`; race_on_test.go explains the skip under -race.
const raceEnabled = false

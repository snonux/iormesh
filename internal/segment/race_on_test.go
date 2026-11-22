//go:build race

package segment

// raceEnabled says the test binary was built with -race. TestReadHostileFile
// skips then: on some flipped footers parquet-go calls unsafe.Slice(nil, n),
// which the race-instrumented runtime turns into a fatal throw ("unsafe.Slice:
// ptr is nil and len is not zero") instead of the recoverable panic the
// reader catches, so the whole test binary dies. race_off_test.go is the
// other half.
const raceEnabled = true

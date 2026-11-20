//go:build ignore

// rowcount prints how many events the published segments of a project
// directory hold: the row counts of every ready/ events file, summed from
// the parquet footers (task ee4). measure.sh needs the emitted-event
// count on the segment path, where no events.ndjson exists to `wc -l`.
// Table files (<name>.<table>.parquet) are skipped: their rows are not
// events.
//
// Usage: go run spikes/s3/rowcount.go PROJECT-DIR
// Throwaway spike code, built with go run (the ignore tag keeps it out
// of the module's packages).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	parquetgo "github.com/parquet-go/parquet-go"
)

// eventsFile matches an events segment's name, which ends in its
// six-digit (or longer) seq right before the extension.
var eventsFile = regexp.MustCompile(`-[0-9]{6,}\.parquet$`)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: rowcount PROJECT-DIR")
		os.Exit(2)
	}
	n, err := count(filepath.Join(os.Args[1], "ready"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "rowcount:", err)
		os.Exit(1)
	}
	fmt.Println(n)
}

// count sums the footer row counts of dir's events files.
func count(dir string) (int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var n int64
	for _, e := range entries {
		if !eventsFile.MatchString(e.Name()) {
			continue
		}
		rows, err := rowsOf(filepath.Join(dir, e.Name()))
		if err != nil {
			return 0, err
		}
		n += rows
	}
	return n, nil
}

func rowsOf(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	pf, err := parquetgo.OpenFile(f, st.Size())
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	return pf.NumRows(), nil
}

// Adapted from ior internal/generate/tracepointsgo.go @ b21afc50328c.
// Changes: emits only List; ior's syscallFamilies and syscallKinds maps
// fed its attach selector, which iormesh does not have (task hj4).

package generate

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var secRe = regexp.MustCompile(`^SEC.*sys_((?:enter|exit)_[a-z_0-9]+)`)

// ExtractTracepoints reads generated C code and extracts tracepoint names from
// SEC annotations, producing the generated_tracepoints.go content: List, the
// tracepoints in source order. ior also emitted per-syscall family and kind
// maps for its -tps/-trace-* attach selector; iormesh selects by its own
// event families (internal/tracepoints/trace_family.go), so it emits neither.
func ExtractTracepoints(r io.Reader) (string, error) {
	tracepoints, err := extractTracepoints(r)
	if err != nil {
		return "", err
	}
	return formatTracepointsGo(tracepoints), nil
}

func extractTracepoints(r io.Reader) ([]string, error) {
	scanner := bufio.NewScanner(r)
	var tracepoints []string

	for scanner.Scan() {
		line := scanner.Text()
		if m := secRe.FindStringSubmatch(line); m != nil {
			tracepoints = append(tracepoints, "sys_"+m[1])
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning input: %w", err)
	}
	return tracepoints, nil
}

func formatTracepointsGo(tracepoints []string) string {
	var b strings.Builder
	b.WriteString("// Code generated - don't change manually!\n")
	b.WriteString("package tracepoints\n\n")
	b.WriteString("var List = []string{\n")
	for _, tp := range tracepoints {
		fmt.Fprintf(&b, "\t%q,\n", tp)
	}
	b.WriteString("}\n")
	return b.String()
}

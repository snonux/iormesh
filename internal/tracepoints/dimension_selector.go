// Adapted from ior internal/tracepoints/dimension_selector.go @ b21afc50328c.
// Changes: import paths ior/ -> iormesh/; the default without a positive
// selector is DefaultSyscalls (M1's set plus the M2 file surface) instead
// of ior's FS family.

package tracepoints

import (
	"fmt"
	"sort"
	"strings"

	"iormesh/internal/csvlist"
	"iormesh/internal/types"
)

// DimensionSelectorConfig holds attach-time syscall-dimension selection inputs.
// Each field accepts comma-separated values.
type DimensionSelectorConfig struct {
	TraceFamilies   string
	TraceKinds      string
	TraceSyscalls   string
	NoTraceFamilies string
	NoTraceKinds    string
	NoTraceSyscalls string
}

// hasAnySelector reports whether any positive or negative dimension selector
// field holds at least one non-blank entry. A field that is empty, blank or
// comma-only counts as unset, exactly as buildAllowedSyscalls treats it.
func (d DimensionSelectorConfig) hasAnySelector() bool {
	for _, raw := range []string{
		d.TraceFamilies, d.TraceKinds, d.TraceSyscalls,
		d.NoTraceFamilies, d.NoTraceKinds, d.NoTraceSyscalls,
	} {
		if len(csvlist.Split(raw)) > 0 {
			return true
		}
	}
	return false
}

// ParseSelectorWithDimensions compiles regex-based attach/exclude filters and
// applies attach-time syscall dimension gating.
//
// Legacy semantics: when the caller supplies an explicit -tps regex but no
// -trace-* / -no-trace-* dimension selectors, the syscall allowlist is skipped
// entirely so that tracepoints outside the default set matched by the regex
// are still attached.
func ParseSelectorWithDimensions(attach, exclude string, dims DimensionSelectorConfig) (Selector, error) {
	sel, err := ParseSelector(attach, exclude)
	if err != nil {
		return Selector{}, err
	}

	// When an explicit -tps regex is provided without any dimension selectors,
	// preserve legacy behaviour: the regex alone controls attachment and the
	// implicit default set is not applied. Decide on the parsed list, not
	// the raw flag: a blank or comma-only -tps yields no regexes and must keep
	// the default set, otherwise the empty Attach list would admit every
	// tracepoint.
	if len(sel.Attach) > 0 && !dims.hasAnySelector() {
		return sel, nil
	}

	allow, err := buildAllowedSyscalls(dims)
	if err != nil {
		return Selector{}, err
	}
	sel.RestrictSyscalls = true
	sel.Syscalls = allow
	return sel, nil
}

// buildAllowedSyscalls resolves the dimension selectors into the set of
// syscalls to attach: the union of the positive -trace-* selectors (or the
// DefaultSyscalls when none is given), minus everything any -no-trace-*
// selector names. Positive selectors are validated before negative ones, so
// an invalid positive entry is the error reported when both are invalid.
func buildAllowedSyscalls(dims DimensionSelectorConfig) (map[string]struct{}, error) {
	knownSyscalls := allKnownSyscalls()
	knownKinds := allKnownKinds()

	include, err := parseDimensionSets(dims.TraceFamilies, dims.TraceKinds, dims.TraceSyscalls,
		knownKinds, knownSyscalls)
	if err != nil {
		return nil, err
	}
	exclude, err := parseDimensionSets(dims.NoTraceFamilies, dims.NoTraceKinds, dims.NoTraceSyscalls,
		knownKinds, knownSyscalls)
	if err != nil {
		return nil, err
	}

	allow := include.includedSyscalls()
	exclude.removeExcluded(allow)
	return allow, nil
}

// dimensionSets is one direction (include or exclude) of the parsed
// family/kind/syscall selectors. provided reports whether any of the three
// fields held at least one entry.
type dimensionSets struct {
	families map[string]struct{}
	kinds    map[string]struct{}
	syscalls map[string]struct{}
	provided bool
}

// parseDimensionSets parses and validates the three comma-separated selector
// fields of one direction, in family, kind, syscall order.
func parseDimensionSets(families, kinds, syscalls string,
	knownKinds, knownSyscalls map[string]struct{}) (dimensionSets, error) {
	var sets dimensionSets
	var familiesProvided, kindsProvided, syscallsProvided bool
	var err error
	if sets.families, familiesProvided, err = parseFamiliesCSV(families); err != nil {
		return dimensionSets{}, err
	}
	if sets.kinds, kindsProvided, err = parseKindsCSV(kinds, knownKinds); err != nil {
		return dimensionSets{}, err
	}
	if sets.syscalls, syscallsProvided, err = parseSyscallsCSV(syscalls, knownSyscalls); err != nil {
		return dimensionSets{}, err
	}
	sets.provided = familiesProvided || kindsProvided || syscallsProvided
	return sets, nil
}

// includedSyscalls returns the syscalls selected by the positive selectors:
// every syscall whose family or kind is listed plus every listed syscall.
// Without any positive selector it falls back to DefaultSyscalls, so an
// unconfigured run attaches only what the current milestone traces (ior
// falls back to its FS family here).
func (d dimensionSets) includedSyscalls() map[string]struct{} {
	if !d.provided {
		return defaultSyscallSet()
	}
	allow := make(map[string]struct{})
	for syscall, family := range syscallFamilies {
		if _, ok := d.families[family]; ok {
			allow[syscall] = struct{}{}
		}
	}
	for syscall, kind := range syscallKinds {
		if _, ok := d.kinds[kind]; ok {
			allow[syscall] = struct{}{}
		}
	}
	for syscall := range d.syscalls {
		allow[syscall] = struct{}{}
	}
	return allow
}

// removeExcluded deletes from allow every syscall the negative selectors name
// directly or through its family or kind. Exclusion always wins over
// inclusion.
func (d dimensionSets) removeExcluded(allow map[string]struct{}) {
	for syscall := range allow {
		if d.excludes(syscall) {
			delete(allow, syscall)
		}
	}
}

// excludes reports whether syscall is named directly, by family, or by kind.
func (d dimensionSets) excludes(syscall string) bool {
	if _, ok := d.syscalls[syscall]; ok {
		return true
	}
	if family, ok := syscallFamilies[syscall]; ok {
		if _, excluded := d.families[family]; excluded {
			return true
		}
	}
	if kind, ok := syscallKinds[syscall]; ok {
		if _, excluded := d.kinds[kind]; excluded {
			return true
		}
	}
	return false
}

func parseFamiliesCSV(raw string) (map[string]struct{}, bool, error) {
	values := csvlist.Split(raw)
	if len(values) == 0 {
		return map[string]struct{}{}, false, nil
	}
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		family, ok := types.ParseSyscallFamily(value)
		if !ok {
			return nil, false, fmt.Errorf("invalid syscall family in trace selector: %q", value)
		}
		out[string(family)] = struct{}{}
	}
	return out, true, nil
}

func parseKindsCSV(raw string, knownKinds map[string]struct{}) (map[string]struct{}, bool, error) {
	values := csvlist.Split(raw)
	if len(values) == 0 {
		return map[string]struct{}{}, false, nil
	}
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		kind := normalizeKind(value)
		if _, ok := knownKinds[kind]; !ok {
			return nil, false, fmt.Errorf("invalid syscall kind in trace selector: %q", value)
		}
		out[kind] = struct{}{}
	}
	return out, true, nil
}

func parseSyscallsCSV(raw string, knownSyscalls map[string]struct{}) (map[string]struct{}, bool, error) {
	values := csvlist.Split(raw)
	if len(values) == 0 {
		return map[string]struct{}{}, false, nil
	}
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		syscall := strings.ToLower(strings.TrimSpace(value))
		if _, ok := knownSyscalls[syscall]; !ok {
			return nil, false, fmt.Errorf("invalid syscall in trace selector: %q", value)
		}
		out[syscall] = struct{}{}
	}
	return out, true, nil
}

func normalizeKind(raw string) string {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	return normalized
}

func allKnownSyscalls() map[string]struct{} {
	out := make(map[string]struct{}, len(syscallFamilies))
	for syscall := range syscallFamilies {
		out[syscall] = struct{}{}
	}
	return out
}

func allKnownKinds() map[string]struct{} {
	out := make(map[string]struct{})
	for _, kind := range syscallKinds {
		if kind == "" {
			continue
		}
		out[kind] = struct{}{}
	}
	return out
}

// KnownKinds returns the sorted, normalized attach-time kind names accepted by
// -trace-kinds and -no-trace-kinds.
func KnownKinds() []string {
	kindSet := allKnownKinds()
	kinds := make([]string, 0, len(kindSet))
	for kind := range kindSet {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}

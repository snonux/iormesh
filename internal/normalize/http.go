package normalize

import (
	"slices"

	"iormesh/internal/schema"
)

// httpMatch compares an http declaration (nil: nothing declared) with the
// event's request: every declared field, and each declared header line
// among the event's.
func httpMatch(want *ExpectedHTTP, got *schema.HTTP) bool {
	if want == nil {
		return true
	}
	if got == nil {
		return false
	}
	for _, f := range [...][2]string{{want.Method, got.Method}, {want.Scheme, got.Scheme},
		{want.Host, got.Host}, {want.Path, got.Path}, {want.Version, got.Version}} {
		if f[0] != "" && f[0] != f[1] {
			return false
		}
	}
	if (want.Status != nil && *want.Status != got.Status) ||
		(want.ReqSeq != nil && *want.ReqSeq != got.ReqSeq) ||
		!sizeMatch(want.ReqBytes, got.ReqBytes) || !sizeMatch(want.RespBytes, got.RespBytes) {
		return false
	}
	return containsAll(got.ReqHeaders, want.ReqHeaders) &&
		containsAll(got.RespHeaders, want.RespHeaders)
}

// sizeMatch compares a declared body size (nil: not declared) with the
// event's (nil: unknown, which no declared size matches).
func sizeMatch(want, got *uint64) bool {
	return want == nil || (got != nil && *want == *got)
}

func containsAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

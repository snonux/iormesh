package k8s

import (
	"strings"
	"testing"
)

// TestParseQuantity: every byte-size form of a resource.Quantity reads
// as its bytes (task 5g4) — binary and decimal SI, a plain byte count, a
// decimal exponent, a fraction — and what is no byte size is refused.
func TestParseQuantity(t *testing.T) {
	for in, want := range map[string]uint64{
		"2Gi": 2 << 30, "512Mi": 512 << 20, "1Ti": 1 << 40, "3Ki": 3 << 10, "1Ei": 1 << 60,
		"2G": 2e9, "2000M": 2e9, "5k": 5000, "1T": 1e12, "1P": 1e15, "1E": 1e18,
		"2147483648": 2 << 30, "0": 0, "2e9": 2e9, "2E9": 2e9, "2e+9": 2e9, "25e-1": 2,
		"1.5Gi": 3 << 29, "1.5G": 15e8, ".5Ki": 512, "1.9": 1, "4Ei": 4 << 60,
	} {
		got, err := parseQuantity(in)
		if err != nil || got != want {
			t.Errorf("parseQuantity(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "Gi", "-1Gi", "+1Gi", "2gi", "2KI", "2m", "2u", "2n", "1 Gi",
		" 1Gi", "1Gi ", "1e", "1e100", "1e-999", "1.2.3", "0x10", "1_000", "5Ei", "99999999999E",
		"1Gi\n", "١Gi", strings.Repeat("1", 41), strings.Repeat("0", 38) + "1Gi"} {
		if got, err := parseQuantity(in); err == nil {
			t.Errorf("parseQuantity(%q) = %d, want an error", in, got)
		}
	}
}

// TestTraceCap: min(¾ limit, limit − 96 MiB) in whole MiB, for any
// byte-size form; under 128Mi, or no size at all, there is no cap (task
// 5g4), and the caller refuses the pod.
func TestTraceCap(t *testing.T) {
	for limit, want := range map[string]string{
		"2Gi": "1536MiB", "512Mi": "384MiB", "1Ti": "786432MiB",
		"2G": "1430MiB", "2000M": "1430MiB", "2147483648": "1536MiB", "2e9": "1430MiB",
		"1.5Gi": "1152MiB", "384Mi": "288MiB", "256Mi": "160MiB", "200Mi": "104MiB",
		"128Mi": "32MiB", "127Mi": "", "100Mi": "", "1Mi": "", "0": "", "2m": "", "": "",
	} {
		if got := TraceCap(limit); got != want {
			t.Errorf("TraceCap(%q) = %q, want %q", limit, got, want)
		}
	}
}

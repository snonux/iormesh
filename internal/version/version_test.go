package version

import "testing"

func TestCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
		bad  bool
	}{
		{"0.1.0", "0.1.0", 0, false},
		{"0.1.0", "0.2.0", -1, false},
		{"1.0.0", "0.99.99", 1, false},
		{"0.10.0", "0.9.0", 1, false},
		{"0.1.10", "0.1.9", 1, false},
		{"v0.1.0", "0.1.0", 0, true},
		{"0.1", "0.1.0", 0, true},
		{"0.01.0", "0.1.0", 0, true},
		{"0.1.0-rc1", "0.1.0", 0, true},
		{"0.+1.0", "0.1.0", 0, true},
		{"0.1.0", "", 0, true},
		{"1234567.0.0", "0.1.0", 0, true},
	}
	for _, tt := range tests {
		got, err := Compare(tt.a, tt.b)
		if (err != nil) != tt.bad {
			t.Errorf("Compare(%q, %q) err = %v, want bad=%v", tt.a, tt.b, err, tt.bad)
			continue
		}
		if !tt.bad && got != tt.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestVersionParses(t *testing.T) {
	if _, err := Parse(Version); err != nil {
		t.Fatalf("Version %q: %v", Version, err)
	}
}

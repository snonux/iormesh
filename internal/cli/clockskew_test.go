package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestTestClockSkew: the test-only skew parses as a signed duration, is
// absent by default, and is refused under the sudoers rule, malformed or
// beyond a day.
func TestTestClockSkew(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		gated       bool
		want        time.Duration
		wantErr     bool
	}{
		{name: "unset", want: 0},
		{name: "unset under sudo", gated: true, want: 0},
		{name: "positive", value: "1.5s", want: 1500 * time.Millisecond},
		{name: "negative", value: "-3m", want: -3 * time.Minute},
		{name: "a day", value: "24h", want: 24 * time.Hour},
		{name: "under sudo", value: "1s", gated: true, wantErr: true},
		{name: "no unit", value: "1500", wantErr: true},
		{name: "garbage", value: "soon", wantErr: true},
		{name: "beyond a day", value: "24h1s", wantErr: true},
		{name: "beyond a day back", value: "-25h", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := func(k string) string {
				if k == testClockSkewEnv {
					return tc.value
				}
				return ""
			}
			got, err := testClockSkew(env, tc.gated)
			if tc.wantErr {
				if !errors.Is(err, errUsage) {
					t.Fatalf("testClockSkew(%q) = %v, %v; want a usage error", tc.value, got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("testClockSkew(%q) = %v, %v; want %v", tc.value, got, err, tc.want)
			}
		})
	}
}

// TestStartRefusesClockSkew: start never forwards the test-only skew to
// its unit, so it refuses one rather than start a session without it —
// and systemd is not called.
func TestStartRefusesClockSkew(t *testing.T) {
	sys := newFakeSystemd()
	c, _ := testControl(sys, map[string]string{testClockSkewEnv: "1s"})
	err := c.start(context.Background(), []string{"-pattern", "x", "-project", "p"})
	if !errors.Is(err, errUsage) || !strings.Contains(err.Error(), testClockSkewEnv) {
		t.Fatalf("start: %v, want a usage error naming %s", err, testClockSkewEnv)
	}
	if len(sys.calls) != 0 {
		t.Fatalf("systemd was called: %v", sys.calls)
	}
}

package javaattach

import (
	"errors"
	"strings"
	"testing"
)

func TestLoadRequest(t *testing.T) {
	for _, tc := range []struct {
		jar, options, want string
	}{
		{"/a/agent.jar", "spool=/s", "1\x00load\x00instrument\x00false\x00/a/agent.jar=spool=/s\x00"},
		{"/a/agent.jar", "", "1\x00load\x00instrument\x00false\x00/a/agent.jar\x00"},
	} {
		if got := string(loadRequest(tc.jar, tc.options)); got != tc.want {
			t.Errorf("loadRequest(%q, %q) = %q, want %q", tc.jar, tc.options, got, tc.want)
		}
	}
}

// TestParseLoadReply covers the replies seen from JDK 17, 21, 25 and
// 27-ea (task ge4) and the shapes jdk.attach also handles.
func TestParseLoadReply(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		wantErr     string // "" = success
		wantIs      error
	}{
		{"loaded (JDK 9+)", "0\nreturn code: 0\n", "", nil},
		{"loaded (JDK 8)", "0\n0\n", "", nil},
		{"agentmain failed", "0\nreturn code: -1\n", "return code -1", nil},
		{"dynamic loading off",
			"0\nDynamic agent loading is not enabled. Use -XX:+EnableDynamicAgentLoading to launch target VM.\n",
			"dynamic agent loading is disabled", ErrDynamicLoadingDisabled},
		{"dynamic loading off, failed status",
			"100\nDynamic agent loading is not enabled.\n",
			"dynamic agent loading is disabled", ErrDynamicLoadingDisabled},
		{"refused", "101\nno such command\n", "refused the load (status 101)", nil},
		{"jar not found", "0\nagent jar not found or no Agent-Class attribute\n", "agent not loaded", nil},
		{"no result", "0\n", "no load result", nil},
		{"empty", "", "unexpected reply", nil},
		{"garbage", "hello\x01", "unexpected reply", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := parseLoadReply(tc.reply)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("parseLoadReply(%q) = %v, want nil", tc.reply, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("parseLoadReply(%q) = %v, want an error with %q", tc.reply, err, tc.wantErr)
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Errorf("parseLoadReply(%q) = %v, want errors.Is %v", tc.reply, err, tc.wantIs)
			}
		})
	}
}

func TestTruncateQuotesAndCuts(t *testing.T) {
	if got := truncate("a\x01b\nsecond"); got != `"a\x01b"` {
		t.Errorf("truncate = %s", got)
	}
	if got := truncate(strings.Repeat("x", 400)); len(got) > 310 {
		t.Errorf("truncate kept %d bytes", len(got))
	}
}

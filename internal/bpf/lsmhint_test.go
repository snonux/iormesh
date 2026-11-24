package bpf

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"testing"
	"unicode/utf8"
)

// fakeHintEnv serves files from a map (a missing key is ENOENT) and a fixed
// memlock limit.
func fakeHintEnv(files map[string]string, memlock uint64) hintEnv {
	return hintEnv{
		readFile: func(path string) ([]byte, error) {
			s, ok := files[path]
			if !ok {
				return nil, fs.ErrNotExist
			}
			return []byte(s), nil
		},
		memlock: func() (uint64, error) { return memlock, nil },
	}
}

const (
	el9Lsms      = "lockdown,capability,yama,selinux,bpf"
	containerCtx = "system_u:system_r:container_t:s0:c123,c456\x00"
)

// TestLoadHint: each host shape names its likeliest cause (task 6h4) -
// the el9 container_t denial of task zg4 names SELinux and its context,
// permissive SELinux does not, and the memlock hint survives only where a
// low limit meets a kernel without memcg accounting.
func TestLoadHint(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		memlock uint64
		want    []string
		notWant []string
	}{
		{"enforcing container_t", map[string]string{
			selinuxEnforcePath: "1\n", selfAttrPath: containerCtx,
			lsmListPath: el9Lsms, osReleasePath: "5.14.0-741.22.1.el9_7.x86_64\n",
		}, 512 << 20, []string{"SELinux is enforcing", "container_t:s0:c123,c456:",
			"docs/decisions.md #13", "audit.log", "memcg"}, []string{"\x00", "raise it"}},
		{"enforcing spc_t", map[string]string{
			selinuxEnforcePath: "1", selfAttrPath: "system_u:system_r:spc_t:s0",
			lsmListPath: "capability,selinux", osReleasePath: "6.12.0",
		}, 512 << 20, []string{"no LSM is confining", "CAP_BPF", "memcg"},
			[]string{"SELinux"}},
		{"permissive", map[string]string{
			selinuxEnforcePath: "0", selfAttrPath: containerCtx,
			lsmListPath: el9Lsms, osReleasePath: "5.14.0",
		}, 512 << 20, []string{"active LSMs that can refuse BPF: lockdown,bpf,"},
			[]string{"SELinux is enforcing", "selinux"}},
		{"no SELinux, no LSM", map[string]string{
			lsmListPath: "capability,yama,landlock", osReleasePath: "6.17.1-300.fc43",
		}, rlimInfinity, []string{"no LSM is confining", "memcg"}, []string{"raise it"}},
		{"low memlock, old kernel", map[string]string{osReleasePath: "5.10.0"},
			64 << 10, []string{"RLIMIT_MEMLOCK is low", "raise it"},
			[]string{"does not apply"}},
		{"low memlock, new kernel", map[string]string{osReleasePath: "5.11.0-rc1"},
			64 << 10, []string{"no LSM is confining", "memcg"},
			[]string{"raise it"}},
		{"high memlock, old kernel", map[string]string{osReleasePath: "4.18.0"},
			512 << 20, []string{"the limit is 512 MiB"}, []string{"raise it"}},
		{"unlimited memlock, old kernel", map[string]string{osReleasePath: "5.4.0"},
			rlimInfinity, []string{"the limit is unlimited"}, []string{"raise it"}},
	}
	for _, tt := range tests {
		got := loadHint(fakeHintEnv(tt.files, tt.memlock))
		for _, w := range tt.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: hint %q lacks %q", tt.name, got, w)
			}
		}
		for _, w := range tt.notWant {
			if strings.Contains(got, w) {
				t.Errorf("%s: hint %q contains %q", tt.name, got, w)
			}
		}
	}
}

// TestLoadHintContextIsSafe: the context is file-derived text, printed
// bounded and with control bytes quoted.
func TestLoadHintContextIsSafe(t *testing.T) {
	ctx := "system_u:system_r:evil_t:s0\x1b[2J" + strings.Repeat("x", 1000)
	got := loadHint(fakeHintEnv(map[string]string{
		selinuxEnforcePath: "1", selfAttrPath: ctx,
	}, rlimInfinity))
	if strings.Contains(got, "\x1b") || len(got) > 800 {
		t.Fatalf("hint not bounded or not terminal-safe (%d bytes): %q", len(got), got)
	}
	if !strings.Contains(got, `\x1b[2J`) {
		t.Fatalf("hint %q does not show the quoted escape", got)
	}
	// A rune across the cut is dropped whole, never printed half.
	ctx = "system_u:system_r:evil_t:" + strings.Repeat("x", hintFieldLimit-26) + "éé"
	got = loadHint(fakeHintEnv(map[string]string{
		selinuxEnforcePath: "1", selfAttrPath: ctx,
	}, rlimInfinity))
	if !utf8.ValidString(got) {
		t.Fatalf("hint cut a rune in half: %q", got)
	}
}

// TestWithLoadHint: only a permission refusal gets the hint, and the errno
// stays visible to errors.Is, so callers fail exactly as before.
func TestWithLoadHint(t *testing.T) {
	env := fakeHintEnv(map[string]string{selinuxEnforcePath: "1", selfAttrPath: containerCtx}, rlimInfinity)
	for _, errno := range []syscall.Errno{syscall.EPERM, syscall.EACCES} {
		err := withLoadHint(fmt.Errorf("failed to load BPF object: %w", errno), env)
		if !errors.Is(err, errno) || !strings.Contains(err.Error(), "SELinux is enforcing") {
			t.Errorf("%v: got %v", errno, err)
		}
	}
	plain := fmt.Errorf("failed to load BPF object: %w", syscall.EINVAL)
	if err := withLoadHint(plain, env); err.Error() != plain.Error() {
		t.Errorf("EINVAL: got %v, want the error unchanged", err)
	}
}

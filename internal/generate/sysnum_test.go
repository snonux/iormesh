package generate

import (
	"strings"
	"testing"
)

const sampleSysnum = `package unix

const (
	SYS_READ    = 0
	SYS_FSTAT   = 5
	SYS_UNAME   = 63
	SYS_UMOUNT2 = 166
)
`

const sampleSysnumC = `SEC("tracepoint/syscalls/sys_enter_read")
SEC("tracepoint/syscalls/sys_exit_read")
SEC("tracepoint/syscalls/sys_enter_newfstat")
SEC("tracepoint/syscalls/sys_enter_newuname")
SEC("tracepoint/syscalls/sys_enter_umount")
SEC("tracepoint/syscalls/sys_enter_open")
`

// TestGenerateSyscallNumbersGo: a tracepoint name resolves directly or
// through its alias (newfstat -> fstat, umount -> umount2), each syscall
// once, and one without a number on the arch is reported, not guessed.
func TestGenerateSyscallNumbersGo(t *testing.T) {
	got, err := GenerateSyscallNumbersGo(strings.NewReader(sampleSysnumC),
		strings.NewReader(sampleSysnum), "amd64", "x/sys test")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"read": 0,`, `"newfstat": 5,`, `"newuname": 63,`,
		`"umount": 166,`, "from x/sys test. DO NOT EDIT.", "their amd64"} {
		if !strings.Contains(got.Source, want) {
			t.Errorf("table lacks %q:\n%s", want, got.Source)
		}
	}
	if strings.Count(got.Source, `"read"`) != 1 {
		t.Errorf("read listed more than once:\n%s", got.Source)
	}
	if len(got.Unresolved) != 1 || got.Unresolved[0] != "open" {
		t.Errorf("unresolved %v, want [open]", got.Unresolved)
	}
}

// TestGenerateSyscallNumbersGoRejectsEmptySource: a file without SYS_
// constants (a wrong path, a renamed x/sys file) is an error, not an empty
// table that would leave every syscall untraced.
func TestGenerateSyscallNumbersGoRejectsEmptySource(t *testing.T) {
	_, err := GenerateSyscallNumbersGo(strings.NewReader(sampleSysnumC),
		strings.NewReader("package unix\n"), "amd64", "x/sys test")
	if err == nil {
		t.Fatal("no error for a source without SYS_ constants")
	}
}

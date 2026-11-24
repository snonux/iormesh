package bpf

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"iormesh/internal/termsafe"
)

// A BPF load the kernel refuses with EPERM makes libbpf print "Couldn't
// load trivial BPF program ... RLIMIT_MEMLOCK", which on a 5.11+ kernel
// (BPF memory charged to the memcg) and with libbpfgo's own 512 MiB bump is
// almost never the cause. On el9 under SELinux enforcing the real one was
// `avc: denied { prog_load } ... container_t ... tclass=bpf`, visible only
// in the raw audit.log (task zg4). loadHint names the likelier cause
// instead (task 6h4); it only adds words to the error, never changes
// whether or how the load fails.

const (
	selinuxEnforcePath = "/sys/fs/selinux/enforce"
	selfAttrPath       = "/proc/self/attr/current"
	lsmListPath        = "/sys/kernel/security/lsm"
	osReleasePath      = "/proc/sys/kernel/osrelease"
	// hintReadLimit bounds every file read for the hint: each is a line.
	hintReadLimit = 512
	// hintFieldLimit bounds each file-derived text in the message.
	hintFieldLimit = 128
	// memlockLow is the RLIMIT_MEMLOCK below which a pre-memcg kernel
	// plausibly refuses the object's maps (libbpfgo asks for 512 MiB).
	memlockLow   = 64 << 20
	rlimInfinity = uint64(unix.RLIM_INFINITY)
)

// hintEnv is what loadHint reads, injectable for tests: readFile returns a
// file's contents (bounded), memlock the soft RLIMIT_MEMLOCK.
type hintEnv struct {
	readFile func(path string) ([]byte, error)
	memlock  func() (uint64, error)
}

// hostHintEnv reads the real files and limit.
func hostHintEnv() hintEnv {
	return hintEnv{readFile: readBounded, memlock: currentMemlock}
}

// readBounded reads at most hintReadLimit bytes of path.
func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, hintReadLimit))
}

// currentMemlock reads the soft RLIMIT_MEMLOCK (after libbpfgo's bump).
func currentMemlock() (uint64, error) {
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_MEMLOCK, &lim); err != nil {
		return 0, err
	}
	return lim.Cur, nil
}

// withLoadHint returns err with a hint appended when it is a permission
// refusal (EPERM or EACCES), else err itself. errors.Is still sees the
// errno through the result.
func withLoadHint(err error, env hintEnv) error {
	if !errors.Is(err, syscall.EPERM) && !errors.Is(err, syscall.EACCES) {
		return err
	}
	return fmt.Errorf("%w; %s", err, loadHint(env))
}

// loadHint names the likeliest reason for a refused load: a confining
// SELinux domain first, then another LSM that can refuse BPF, then a low
// memlock limit on a kernel without memcg accounting, else capabilities.
func loadHint(env hintEnv) string {
	low, note := memlockVerdict(env)
	if ctx, ok := confiningSELinux(env); ok {
		return "BPF load refused; SELinux is enforcing and this process " +
			"runs as " + ctx + ": a container_t tracer needs the el9 " +
			"profile (docs/decisions.md #13, task 5h4), see docs/security.md; the " +
			"denial (avc ... tclass=bpf) shows in the raw audit.log, " +
			"not always in ausearch -m avc" + note
	}
	if lsms := bpfLSMs(env); lsms != "" {
		return "BPF load refused; active LSMs that can refuse BPF: " +
			lsms + ", see docs/security.md" + note
	}
	if low {
		return "BPF load refused; RLIMIT_MEMLOCK is low on a kernel " +
			"before 5.11 (no memcg accounting): raise it (ulimit -l " +
			"unlimited, or CAP_SYS_RESOURCE for libbpfgo's bump)"
	}
	return "BPF load refused; no LSM is confining it, check the " +
		"capabilities (CAP_BPF and CAP_PERFMON, or CAP_SYS_ADMIN)" + note
}

// memlockVerdict reports whether a low memlock limit plausibly caused the
// refusal: a soft limit under memlockLow on a kernel before 5.11 (or one
// whose release is unreadable). When it did not, note is a clause saying
// libbpf's RLIMIT_MEMLOCK hint does not apply, and why; an unreadable
// limit yields neither.
func memlockVerdict(env hintEnv) (low bool, note string) {
	const lead = " (libbpf's RLIMIT_MEMLOCK hint does not apply: "
	if major, minor, ok := kernelVersion(env); ok && (major > 5 || major == 5 && minor >= 11) {
		return false, lead + "this kernel charges BPF memory to the memcg)"
	}
	cur, err := env.memlock()
	switch {
	case err != nil:
		return false, ""
	case cur == rlimInfinity:
		return false, lead + "the limit is unlimited)"
	case cur >= memlockLow:
		return false, lead + "the limit is " + strconv.FormatUint(cur>>20, 10) + " MiB)"
	}
	return true, ""
}

// confiningSELinux reports SELinux enforcing with this process in a domain
// that is not unconfined (spc_t, unconfined_t, kernel_t pass BPF), and the
// context to print, terminal-safe and bounded.
func confiningSELinux(env hintEnv) (string, bool) {
	enforce, err := env.readFile(selinuxEnforcePath)
	if err != nil || strings.TrimSpace(string(enforce)) != "1" {
		return "", false
	}
	raw, err := env.readFile(selfAttrPath)
	ctx := strings.TrimSpace(strings.TrimRight(string(raw), "\x00\n"))
	if err != nil || ctx == "" {
		return "an unknown context", true
	}
	if parts := strings.Split(ctx, ":"); len(parts) >= 3 {
		switch parts[2] {
		case "spc_t", "unconfined_t", "kernel_t":
			return "", false
		}
	}
	return bounded(ctx), true
}

// bpfLSMs lists the active LSMs besides SELinux that can refuse a BPF load
// (AppArmor's bpf rules, BPF LSM programs, lockdown), "" for none.
func bpfLSMs(env hintEnv) string {
	raw, err := env.readFile(lsmListPath)
	if err != nil {
		return ""
	}
	var found []string
	for _, name := range strings.Split(strings.TrimSpace(string(raw)), ",") {
		switch name {
		case "apparmor", "bpf", "lockdown":
			found = append(found, name)
		}
	}
	return strings.Join(found, ",")
}

// kernelVersion parses the running kernel's major and minor release.
func kernelVersion(env hintEnv) (int, int, bool) {
	raw, err := env.readFile(osReleasePath)
	if err != nil {
		return 0, 0, false
	}
	fields := strings.SplitN(strings.TrimSpace(string(raw)), ".", 3)
	if len(fields) < 2 {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(fields[0])
	// The minor may carry a suffix when there is no patch level ("11-rc1").
	minor, err2 := strconv.Atoi(leadingDigits(fields[1]))
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return major, minor, true
}

func leadingDigits(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

// bounded cuts s to hintFieldLimit bytes and makes it terminal-safe.
func bounded(s string) string {
	if len(s) > hintFieldLimit {
		// Cut on a rune boundary: half a rune would print as raw bytes.
		cut := hintFieldLimit
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "..."
	}
	return termsafe.String(s)
}

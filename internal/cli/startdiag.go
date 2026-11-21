package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"iormesh/internal/termsafe"
)

// Why a start's systemd-run failed (task rf4). systemd-run --quiet
// --service-type=exec exits 1 with nothing on stderr when systemd cannot
// exec the binary: the cause — "Failed to locate executable ...:
// Permission denied", status 203/EXEC — is only in the unit's journal,
// and --collect has already dropped the unit, so `systemctl status`
// knows nothing either. On el9 the usual cause is SELinux: systemd
// (init_t) may not exec a binary labelled admin_home_t, i.e. one under
// /root. start therefore appends the unit's journal tail and, when
// SELinux is enforcing, a hint naming the binary's label. The hint does
// not wait for an AVC record: el9's policy does not audit this denial
// (`ausearch -m avc` shows nothing), so the label is the evidence.
const (
	// diagLines bounds the journal tail, diagLineBytes each line and
	// diagBytes the whole diagnosis: an error message, not a log dump.
	diagLines     = 20
	diagLineBytes = 300
	diagBytes     = 4096
)

// execTypes are the SELinux file types systemd execs from without a
// policy change: /usr/bin, /usr/local/bin (bin_t) and /opt (usr_t).
var execTypes = map[string]bool{"bin_t": true, "usr_t": true}

// selinuxState is what the hint needs: whether SELinux enforces, and the
// binary's file context ("" when unreadable or unlabelled).
type selinuxState struct {
	enforcing bool
	label     string
}

// hostSELinux reads the state from selinuxfs and the binary's
// security.selinux xattr. Every failure reads as "no SELinux".
func hostSELinux(path string) selinuxState {
	var st selinuxState
	if b, err := os.ReadFile("/sys/fs/selinux/enforce"); err == nil {
		st.enforcing = strings.TrimSpace(string(b)) == "1"
	}
	buf := make([]byte, 256)
	if n, err := unix.Getxattr(path, "security.selinux", buf); err == nil {
		st.label = strings.TrimRight(string(buf[:n]), "\x00")
	}
	return st
}

// diagnoseStart wraps runErr, systemd-run's failure for unit started at
// since, with the unit's journal since then (or, if that is empty, its
// `systemctl status`) and an SELinux hint. The added text is bounded and
// terminal-safe: it carries the binary's path and systemd's own words.
func (c control) diagnoseStart(ctx context.Context, unit string, since time.Time, runErr error) error {
	tail := c.unitLog(ctx, unit, since)
	var b strings.Builder
	if tail != "" {
		b.WriteString("\n" + unit + " logged:\n" + tail)
	}
	if hint := c.selinuxHint(tail); hint != "" {
		b.WriteString("\n" + hint)
	}
	if b.Len() == 0 {
		return runErr
	}
	return fmt.Errorf("%w%s", runErr, b.String())
}

// unitLog is the unit's journal tail since since, falling back to
// `systemctl status` (which exits non-zero for a failed unit but still
// prints it). Neither is an error of its own: the diagnosis is best
// effort, and systemd-run's error stays the one returned.
func (c control) unitLog(ctx context.Context, unit string, since time.Time) string {
	out, _ := c.sys.Output(ctx, "journalctl", "--no-pager", "--quiet", "-o", "short-iso",
		"-n", strconv.Itoa(diagLines), "--since=@"+strconv.FormatInt(since.Unix(), 10),
		"--unit="+unit)
	if tail := boundedLines(out); tail != "" {
		return tail
	}
	out, _ = c.sys.Output(ctx, "systemctl", "status", "--no-pager", "--full",
		"--lines="+strconv.Itoa(diagLines), "--", unit)
	return boundedLines(out)
}

// boundedLines keeps the last diagLines non-blank lines of out, each cut
// to diagLineBytes, made terminal-safe and indented, and of those only as
// many of the last as fit diagBytes.
func boundedLines(out []byte) string {
	var lines []string
	for line := range strings.Lines(string(out)) {
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) > diagLineBytes {
			line = line[:diagLineBytes] + "..."
		}
		lines = append(lines, "  "+termsafe.String(line))
	}
	if len(lines) > diagLines {
		lines = lines[len(lines)-diagLines:]
	}
	// Over diagBytes the oldest lines go: systemd logs the cause last.
	for len(lines) > 1 && len(strings.Join(lines, "\n")) > diagBytes {
		lines = lines[1:]
	}
	return strings.Join(lines, "\n")
}

// selinuxHint names SELinux as the likely cause when it enforces and
// either the binary's type is not one systemd execs from, or the unit's
// log shows a permission or AVC denial.
func (c control) selinuxHint(tail string) string {
	if c.selinux == nil {
		return ""
	}
	st := c.selinux(c.self)
	if !st.enforcing {
		return ""
	}
	typ := labelType(st.label)
	denied := strings.Contains(tail, "Permission denied") || strings.Contains(tail, "avc:")
	if (typ == "" || execTypes[typ]) && !denied {
		return ""
	}
	what := "SELinux is enforcing"
	if typ != "" {
		what += fmt.Sprintf(" and %s is labelled %s", termsafe.String(c.self), termsafe.String(typ))
	}
	return what + ": systemd may not exec a binary outside a bin_t or usr_t path " +
		"(e.g. under /root, admin_home_t); install it under /usr/local/bin or /opt " +
		"(restorecon relabels it)"
}

// labelType is the type field of an SELinux context user:role:type:level.
func labelType(label string) string {
	f := strings.SplitN(label, ":", 4)
	if len(f) < 3 {
		return ""
	}
	return f[2]
}

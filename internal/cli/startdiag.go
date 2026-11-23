package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
	// diagLines bounds the journal tail, diagLineBytes each line as
	// printed (after termsafe quoting, which can quadruple a line) and
	// diagBytes the whole diagnosis: an error message, not a log dump.
	// diagBytes also keeps the error within the 2048 bytes of a remote
	// command's stderr that iormeshctl deploy and ensure keep (task zk4,
	// internal/federate stderrLimit): that cut keeps the head, and the
	// cause is logged last. 1536 leaves room for the error's own text
	// with the longest unit name (TestDiagnoseExitFitsRelay).
	diagLines     = 20
	diagLineBytes = 300
	diagBytes     = 1536
	// diagTimeout bounds each diagnostic command (task 8g4): a hung
	// journald must not hold up start's error, which is the point.
	diagTimeout = 5 * time.Second
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
	return withUnitLog(runErr, unit, tail, c.selinuxHint(tail))
}

// diagnoseExit wraps exitErr, a unit started at since that exited before
// its run attached, with the unit's journal tail (task zk4): the run
// logs why it gave up there — "target spec: nothing listens on TCP port
// N" for a -listen-port whose server is down — and start's caller
// (iormeshctl deploy and ensure, on another host) sees only this error.
// No SELinux hint: with --service-type=exec, systemd-run already failed
// if systemd could not exec the binary, so this run did start.
func (c control) diagnoseExit(ctx context.Context, unit string, since time.Time, exitErr error) error {
	return withUnitLog(exitErr, unit, c.unitLog(ctx, unit, since), "")
}

// withUnitLog appends tail (already bounded and terminal-safe, see
// unitLog) and hint to err; with neither, err is returned as it is.
func withUnitLog(err error, unit, tail, hint string) error {
	var b strings.Builder
	if tail != "" {
		b.WriteString("\n" + unit + " logged:\n" + tail)
	}
	if hint != "" {
		b.WriteString("\n" + hint)
	}
	if b.Len() == 0 {
		return err
	}
	return fmt.Errorf("%w%s", err, b.String())
}

// unitLog is the unit's journal tail since since, falling back to
// `systemctl status` (which exits non-zero for a failed unit but still
// prints it). Neither is an error of its own: the diagnosis is best
// effort, and systemd-run's error stays the one returned.
//
// The fallback passes --lines=0, the unit's state without its journal
// (task 8g4). Our unit is --collect'ed once it fails, so a unit status
// still finds is most likely a racing start's of the same project —
// systemd-run's "already loaded" — and its log lines are that other
// session's, not ours. Its state header still says who holds the name.
func (c control) unitLog(ctx context.Context, unit string, since time.Time) string {
	out := c.diagOutput(ctx, "journalctl", "--no-pager", "--quiet", "-o", "short-iso",
		"-n", strconv.Itoa(diagLines), "--since=@"+strconv.FormatInt(since.Unix(), 10),
		"--unit="+unit)
	if tail := boundedLines(out); tail != "" {
		return tail
	}
	return boundedLines(c.diagOutput(ctx, "systemctl", "status", "--no-pager", "--full", "--lines=0", "--", unit))
}

// diagOutput runs one diagnostic command under diagTimeout and returns
// its stdout, whatever it exited with.
func (c control) diagOutput(ctx context.Context, name string, args ...string) []byte {
	ctx, cancel := context.WithTimeout(ctx, diagTimeout)
	defer cancel()
	out, _ := c.sys.Output(ctx, name, args...)
	return out
}

// boundedLines keeps the last diagLines non-blank lines of out, each
// made terminal-safe, then cut to diagLineBytes (cutLine) and indented,
// and of those only as many of the last as fit diagBytes.
func boundedLines(out []byte) string {
	var lines []string
	for line := range strings.Lines(string(out)) {
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, "  "+cutLine(termsafe.String(line)))
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

// cutLine cuts an already terminal-safe line to diagLineBytes plus
// "...". Cutting after the quoting (task 8g4) bounds what is printed: a
// line of control bytes quotes to four bytes each. The cut backs off to
// a rune boundary, so a printable multi-byte rune is never split into
// invalid UTF-8; a cut inside a quoted escape leaves printable ASCII.
func cutLine(s string) string {
	if len(s) <= diagLineBytes {
		return s
	}
	n := diagLineBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
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

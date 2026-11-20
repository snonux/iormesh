package federate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
)

// SSH is the Transport for plain Linux servers (decision #12). It wraps the
// system ssh and rsync binaries instead of an SSH library, so the
// operator's ~/.ssh/config, agent, jump hosts and known_hosts just work,
// and iormeshctl never holds a key or a password itself.
//
// Every call goes through one multiplexed connection per host
// (ControlMaster): deploy and collect run several commands back to back,
// and one handshake per command would dominate them.
type SSH struct {
	host       string
	controlDir string
	exec       execFunc
}

// execFunc runs one local command and returns its stdout. SSH takes it as a
// field so tests see the exact argv without reaching any host.
type execFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// ErrTransport marks an argument the transport refuses before running
// anything.
var ErrTransport = errors.New("transport")

// remotePath is the alphabet Fetch accepts for remote directories. They
// are iormesh's own paths (/opt/iormesh/traces/<project>/ready), and rsync
// hands the remote side of host:path to the remote shell on older
// versions, so nothing a shell would interpret may appear.
var remotePath = regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`)

// stagePath is the alphabet Push accepts for its target directory: the
// same characters as remotePath, but relative paths (under the login
// user's home) too, as long as they cannot read as an option.
var stagePath = regexp.MustCompile(`^[A-Za-z0-9._/][A-Za-z0-9._/-]*$`)

// stderrLimit bounds how much of a failed command's stderr an error
// carries: enough for ssh's diagnosis, not a remote log dump.
const stderrLimit = 2048

// waitDelay is runLocal's cmd.WaitDelay (see there).
const waitDelay = 2 * time.Second

// Unix socket path limits for the control sockets: sun_path holds 104
// bytes on macOS (108 on Linux; the smaller one counts), and a socket
// name is "/" + %C (40 hex digits) + the ".<16 random>" suffix ssh binds
// first and renames from.
const (
	sunPathMax     = 104
	controlNameLen = 1 + 40 + 17
)

// SSH satisfies Transport; checked at compile time.
var _ Transport = SSH{}

// NewSSH returns the SSH transport for host, a registry host name. The
// control sockets live in a private directory under the user cache dir
// (see ControlDir).
func NewSSH(host string) (SSH, error) {
	dir, err := ControlDir()
	if err != nil {
		return SSH{}, err
	}
	return newSSH(host, dir, runLocal)
}

// newSSH is NewSSH with the control directory and the command runner
// injected.
func newSSH(host, controlDir string, ex execFunc) (SSH, error) {
	if !ValidHost(host) {
		return SSH{}, fmt.Errorf("%w: host %q", ErrTransport, host)
	}
	// The directory is quoted inside rsync's -e string, whose parser
	// knows single quotes but no escapes.
	if strings.ContainsAny(controlDir, "'\"") {
		return SSH{}, fmt.Errorf("%w: control dir %q contains a quote", ErrTransport, controlDir)
	}
	return SSH{host: host, controlDir: controlDir, exec: ex}, nil
}

// ControlDir returns, creating it if needed, the directory holding the ssh
// control sockets (controlBase). A control socket is a live,
// authenticated session to a server, so the directory must be the user's
// own and closed to everyone else; one that is not is refused rather than
// repaired, since someone else may already hold a socket in it.
func ControlDir() (string, error) {
	dir, err := controlBase()
	if err != nil {
		return "", fmt.Errorf("ssh control dir: %w", err)
	}
	if n := len(dir) + controlNameLen; n > sunPathMax {
		return "", fmt.Errorf("%w: control socket path under %s would be %d bytes, over the %d a unix socket allows",
			ErrTransport, dir, n, sunPathMax)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("ssh control dir: %w", err)
	}
	return dir, checkPrivateDir(dir)
}

// Run executes argv on the host through ssh. ssh joins its command
// arguments with spaces and hands the result to the remote login shell,
// so every element is single-quoted first: an argument stays one argument
// and nothing in it is expanded remotely.
//
// That quoting is POSIX shell quoting, so the deploying user's login shell
// on the server must be a POSIX shell (sh, bash, dash, zsh, ksh): csh and
// tcsh still read ! and newlines inside single quotes. The registry
// refuses control characters in what it stores (Project.validate), so a
// newline never gets this far from there.
func (s SSH) Run(ctx context.Context, argv ...string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("%w: empty remote command", ErrTransport)
	}
	args := append(s.sshOptions(), "--", s.host, quoteArgv(argv))
	out, err := s.exec(ctx, "ssh", args...)
	if err != nil {
		return out, fmt.Errorf("ssh %s %s: %w", s.host, argv[0], err)
	}
	return out, nil
}

// Fetch moves remoteDir's segment files (FetchFlags: the non-dot
// *.parquet files directly in it) into localDir with rsync over the same
// multiplexed ssh connection. --remove-source-files deletes a remote file
// only after it transferred, the delete-on-transfer of PLAN.md M4
// "Collect".
//
// The server is not trusted with anything on the controller beyond file
// contents, so rsync runs -rt, not -a: without -p the remote modes do not
// carry over (a 0755 ready/ would otherwise open localDir to everyone,
// a 0666 segment stay 0666 here), and without -l a symlink the server
// planted (lnk -> /etc/passwd) is skipped rather than recreated for a
// later reader to follow. localDir is then forced to 0700 — traces carry
// command lines, paths and endpoints, readable by the operator only — so
// whatever modes the files get (source masked by umask) they sit behind
// a closed directory. (rsync's --chmod would say it per file, but macOS's
// rsync/openrsync do not all take it.)
func (s SSH) Fetch(ctx context.Context, remoteDir, localDir string) error {
	if !remotePath.MatchString(remoteDir) || strings.Contains(remoteDir, "..") {
		return fmt.Errorf("%w: remote dir %q", ErrTransport, remoteDir)
	}
	// Absolute, so rsync can never read a colon in it as host:path.
	local, err := filepath.Abs(localDir)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	if err := os.MkdirAll(local, 0o700); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	args := slices.Concat(FetchFlags(), []string{"-e", s.rsyncShell(), "--",
		s.host + ":" + strings.TrimSuffix(remoteDir, "/") + "/", local + "/"})
	_, err = s.exec(ctx, "rsync", args...)
	// Chmod even after a failed rsync: a partial transfer is data too.
	if cerr := os.Chmod(local, 0o700); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("rsync %s:%s: %w", s.host, remoteDir, err)
	}
	return nil
}

// FetchFlags are the rsync flags of Fetch, before -e and the paths;
// exported so a test standing in for the server runs exactly them:
//   - -rt: recurse into remoteDir and keep mtimes; no -p (remote modes),
//     no -l (a symlink is skipped, never recreated), no -o/-g;
//   - --remove-source-files: delete a remote file once transferred;
//   - --exclude=.*: never a dot file — the tracer publishes none, and in
//     the local store dot files are rsync's own in-flight temporaries,
//     which the build skips;
//   - --include=*.parquet --exclude=*: segment files only, directly in
//     remoteDir; the trailing exclude also matches every directory, so a
//     subdirectory planted in ready/ is never descended into.
func FetchFlags() []string {
	return []string{"-rt", "--remove-source-files", "--exclude=.*", "--include=*.parquet", "--exclude=*"}
}

// Push copies localFiles into remoteDir with rsync over the multiplexed
// ssh connection. -c compares checksums instead of size and mtime: the
// files are a tracer build and its signature, cheap to hash, and a staged
// copy damaged in place (same size, same mtime) is then sent again rather
// than kept; an intact one still transfers nothing. -t keeps the mtime.
// As in Fetch, no -p,
// -o or -l: the files arrive as the login user's own, with the remote
// umask, which is what `iormesh upgrade` requires of a staged file (owned
// by the sudo caller). Each local file is made absolute, so a colon in
// its name is never read as host:path.
func (s SSH) Push(ctx context.Context, remoteDir string, localFiles ...string) error {
	if !stagePath.MatchString(remoteDir) || strings.Contains(remoteDir, "..") {
		return fmt.Errorf("%w: remote dir %q", ErrTransport, remoteDir)
	}
	if len(localFiles) == 0 {
		return fmt.Errorf("%w: nothing to push", ErrTransport)
	}
	args := []string{"-ct", "-e", s.rsyncShell(), "--"}
	for _, f := range localFiles {
		abs, err := filepath.Abs(f)
		if err != nil {
			return fmt.Errorf("push: %w", err)
		}
		args = append(args, abs)
	}
	args = append(args, s.host+":"+strings.TrimSuffix(remoteDir, "/")+"/")
	if _, err := s.exec(ctx, "rsync", args...); err != nil {
		return fmt.Errorf("rsync to %s:%s: %w", s.host, remoteDir, err)
	}
	return nil
}

// sshOptions are the options every ssh invocation carries, Run's and the
// one rsync starts. Command-line options win over ~/.ssh/config, so these
// are the few iormeshctl insists on:
//   - BatchMode=yes: key or agent only, never a password or passphrase
//     prompt that would hang a collect loop;
//   - StrictHostKeyChecking=yes: the server's key must already be in
//     known_hosts. An unknown or changed key fails instead of being
//     trusted on first use — deploy pushes a root-run binary, the one
//     place a spoofed server must not be accepted silently;
//   - ControlMaster/ControlPath/ControlPersist: one connection per host,
//     reused by the next command for a minute; %C hashes user, host and
//     port, so the socket path stays short (macOS caps it at 104 bytes).
func (s SSH) sshOptions() []string {
	return []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + filepath.Join(s.controlDir, "%C"),
		"-o", "ControlPersist=60s",
		"-o", "ConnectTimeout=15",
	}
}

// rsyncShell is rsync's -e value: ssh with sshOptions. rsync splits it on
// spaces and honors single quotes, so each element is quoted.
func (s SSH) rsyncShell() string {
	return "ssh " + quoteArgv(s.sshOptions())
}

// quoteArgv renders argv for a POSIX shell: each element single-quoted,
// and an embedded single quote closed, escaped and reopened:
//
//	it's  ->  'it'\''s'
//
// The remote shell then sees exactly argv.
func quoteArgv(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

// controlBase is where the control sockets go: <user cache dir>/iormesh/ssh,
// except on macOS, whose cache dir (~/Library/Caches) leaves too few of
// sun_path's 104 bytes for a longer user name; ~/.iormesh/ssh there.
func controlBase() (string, error) {
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".iormesh", "ssh"), nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "iormesh", "ssh"), nil
}

// runLocal is the real execFunc. stdin stays /dev/null, so ssh never reads
// the operator's terminal; a failure carries the start of stderr, which is
// where ssh explains itself (host key mismatch, permission denied).
//
// WaitDelay guards against the ControlPersist master, which ssh forks off
// the first command: OpenSSH points its stdio at /dev/null when it
// detaches, but should one ever keep our pipe open, Wait would block for
// the whole persist time. Once the command itself has exited its output
// is complete, so ErrWaitDelay counts as success.
func runLocal(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = waitDelay
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > stderrLimit {
			msg = msg[:stderrLimit] + "…"
		}
		return stdout.Bytes(), fmt.Errorf("%w: %s", err, msg)
	}
	return stdout.Bytes(), err
}

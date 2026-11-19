package javaattach

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// DefaultTimeout bounds one attach when the caller gives no deadline of
// its own. S1 measured ≈150–175 ms for the handshake plus the agent's
// install on a loaded host; the rest is room for a busy target.
const DefaultTimeout = 10 * time.Second

// socketPoll is how often the socket's appearance is checked after
// SIGQUIT (the JDK's attacher starts at 100 ms; the listener usually
// takes a few milliseconds).
const socketPoll = 20 * time.Millisecond

var (
	// ErrNotJVM: the process maps no libjvm.so, so it does not speak
	// the protocol and SIGQUIT would kill it.
	ErrNotJVM = errors.New("not a HotSpot JVM (no libjvm.so mapped)")
	// ErrNoQuitHandler: the JVM has no SIGQUIT handler installed — it is
	// still starting, or runs with -Xrs — so SIGQUIT would kill it.
	ErrNoQuitHandler = errors.New("the JVM has no SIGQUIT handler (still starting, or -Xrs)")
	// ErrAttachDisabled: the JVM runs with -XX:+DisableAttachMechanism.
	ErrAttachDisabled = errors.New("the JVM runs with " + disableAttachFlag)
)

// Request names the agent to load and the JVM to load it into.
type Request struct {
	PID uint32
	// Jar is the agent jar's absolute path as the target sees it (inside
	// its mount namespace).
	Jar string
	// Options is the agent's option string, the part after "=" in
	// -javaagent:jar=options.
	Options string
}

// Attacher performs the handshake. The zero value is not usable; New
// returns the host's.
type Attacher struct {
	// ProcRoot is /proc; tests point it at a fake tree.
	ProcRoot string
	// Signal delivers SIGQUIT to the process; tests replace it.
	Signal func(pid uint32) error
}

// New returns the attacher for the host's processes.
func New() Attacher {
	return Attacher{ProcRoot: "/proc", Signal: sigquit}
}

// Load loads r's agent into r.PID's JVM and returns once the JVM
// reported the agent started, or failed with the JVM's own reason. ctx
// bounds the whole attach (DefaultTimeout when it has no deadline).
func (a Attacher) Load(ctx context.Context, r Request) error {
	if !filepath.IsAbs(r.Jar) {
		return fmt.Errorf("attach: agent jar %q is not absolute", r.Jar)
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}
	info, err := a.check(r.PID)
	if err != nil {
		return fmt.Errorf("attach pid %d: %w", r.PID, err)
	}
	conn, err := a.connect(ctx, r.PID, info)
	if err != nil {
		return fmt.Errorf("attach pid %d: %w", r.PID, err)
	}
	defer func() { _ = conn.Close() }()
	if err := exchange(ctx, conn, loadRequest(r.Jar, r.Options)); err != nil {
		return fmt.Errorf("attach pid %d: %w", r.PID, err)
	}
	return nil
}

// check refuses a target that cannot take the handshake safely.
func (a Attacher) check(pid uint32) (procInfo, error) {
	jvm, err := mapsJVM(a.ProcRoot, pid)
	if err != nil {
		return procInfo{}, err
	}
	if !jvm {
		return procInfo{}, ErrNotJVM
	}
	info, err := readProcInfo(a.ProcRoot, pid)
	if err != nil {
		return procInfo{}, err
	}
	if attachDisabled(a.ProcRoot, pid) {
		return procInfo{}, ErrAttachDisabled
	}
	return info, nil
}

// connect returns a connection to the JVM's attach socket, starting the
// JVM's attach listener first if the socket does not exist yet.
func (a Attacher) connect(ctx context.Context, pid uint32, info procInfo) (net.Conn, error) {
	sock := a.socketPath(pid, info)
	if _, err := os.Lstat(sock); errors.Is(err, os.ErrNotExist) {
		if err := a.startListener(ctx, pid, info, sock); err != nil {
			return nil, err
		}
	}
	if err := checkSocket(sock, info.euid); err != nil {
		return nil, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", sock, err)
	}
	return conn, nil
}

// socketPath is /tmp/.java_pid<nspid> in the JVM's root: HotSpot puts it
// in /tmp whatever java.io.tmpdir says.
func (a Attacher) socketPath(pid uint32, info procInfo) string {
	return filepath.Join(procPath(a.ProcRoot, pid, "root"), "tmp",
		".java_pid"+strconv.FormatUint(uint64(info.nspid), 10))
}

// startListener creates the trigger file, sends the one SIGQUIT and waits
// for the socket; the trigger file is removed again either way.
func (a Attacher) startListener(ctx context.Context, pid uint32, info procInfo, sock string) error {
	if !info.quitCaught {
		return ErrNoQuitHandler
	}
	remove, err := a.createTrigger(pid, info)
	if err != nil {
		return err
	}
	defer remove()
	if err := a.Signal(pid); err != nil {
		return fmt.Errorf("SIGQUIT: %w", err)
	}
	return waitSocket(ctx, sock)
}

// createTrigger creates .attach_pid<nspid> in the JVM's working directory
// or, when that is not writable, in its /tmp — where HotSpot's SIGQUIT
// handler looks, in that order. O_EXCL|O_NOFOLLOW: the directories
// belong to the target, so a planted symlink is never followed and a
// file not created here is never removed. The JVM accepts a trigger
// owned by its own uid or root.
func (a Attacher) createTrigger(pid uint32, info procInfo) (remove func(), err error) {
	name := ".attach_pid" + strconv.FormatUint(uint64(info.nspid), 10)
	var errs []error
	for _, dir := range []string{procPath(a.ProcRoot, pid, "cwd"),
		filepath.Join(procPath(a.ProcRoot, pid, "root"), "tmp")} {
		path := filepath.Join(dir, name)
		fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|
			unix.O_CLOEXEC|unix.O_NONBLOCK, 0o600)
		if errors.Is(err, unix.EEXIST) {
			return func() {}, nil // another attacher's: it serves us too
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("trigger %s: %w", path, err))
			continue
		}
		_ = unix.Close(fd)
		return func() { _ = os.Remove(path) }, nil
	}
	return nil, errors.Join(errs...)
}

// waitSocket polls for the socket until ctx ends.
func waitSocket(ctx context.Context, sock string) error {
	tick := time.NewTicker(socketPoll)
	defer tick.Stop()
	for {
		if _, err := os.Lstat(sock); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("no attach socket %s after SIGQUIT: %w", sock, ctx.Err())
		case <-tick.C:
		}
	}
}

// checkSocket verifies the socket is one and belongs to the JVM's uid (or
// root): /tmp is shared, and another user could plant a socket there to
// receive the request.
func checkSocket(sock string, euid uint32) error {
	var st unix.Stat_t
	if err := unix.Lstat(sock, &st); err != nil {
		return fmt.Errorf("attach socket: %w", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return fmt.Errorf("attach socket %s is not a socket", sock)
	}
	if st.Uid != euid && st.Uid != 0 {
		return fmt.Errorf("attach socket %s belongs to uid %d, not the JVM's %d", sock, st.Uid, euid)
	}
	return nil
}

// exchange sends req and judges the reply, both within ctx's deadline.
func exchange(ctx context.Context, conn net.Conn, req []byte) error {
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return err
		}
	}
	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("send load request: %w", err)
	}
	reply, err := readReply(conn)
	if err != nil {
		return fmt.Errorf("read reply (the agent may still finish loading): %w", err)
	}
	return parseLoadReply(reply)
}

// sigquit sends SIGQUIT through a pidfd, so a pid recycled since the
// checks is never signalled — the pidfd refers to the process the pid
// named when it was opened, and fails with ESRCH once that one is gone.
// The checks ran before the pidfd was opened, so the pidfd's process is
// re-checked to be a JVM with a SIGQUIT handler before signalling.
func sigquit(pid uint32) error {
	fd, err := unix.PidfdOpen(int(pid), 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	info, err := readProcInfo("/proc", pid)
	if err != nil {
		return err
	}
	jvm, err := mapsJVM("/proc", pid)
	switch {
	case err != nil:
		return err
	case !jvm:
		return ErrNotJVM
	case !info.quitCaught:
		return ErrNoQuitHandler
	}
	return unix.PidfdSendSignal(fd, syscall.SIGQUIT, nil, 0)
}

package javaattach

import (
	"context"
	"errors"
	"fmt"
	"net"
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
	// ErrWrongPeer: the process behind the attach socket is not the
	// target — its socket path led elsewhere (a planted symlink).
	ErrWrongPeer = errors.New("the attach socket's peer is not the target JVM")
	// ErrForeignTrigger: a trigger file is there already but neither
	// root's nor the JVM's user's; the JVM would ignore it and answer
	// SIGQUIT with a thread dump, so no signal is sent.
	ErrForeignTrigger = errors.New("a trigger file of another user is in the way")
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
// JVM's attach listener first if the socket does not exist yet. Every
// path inside the target is reached through directory fds (jvmDirs), so
// a symlink the target planted never leads the tracer, root, into the
// host's tree; and the connected peer must be the target process itself
// (checkPeer) — the socket's path is the target's to point anywhere.
func (a Attacher) connect(ctx context.Context, pid uint32, info procInfo) (net.Conn, error) {
	dirs, err := a.openDirs(pid)
	if err != nil {
		return nil, err
	}
	defer dirs.close()
	name := ".java_pid" + strconv.FormatUint(uint64(info.nspid), 10)
	if _, err := dirs.statTmp(name); errors.Is(err, unix.ENOENT) {
		if err := a.startListener(ctx, pid, info, dirs, name); err != nil {
			return nil, err
		}
	}
	if err := dirs.checkSocket(name); err != nil {
		return nil, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", dirs.tmpPath(name))
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", name, err)
	}
	if err := checkPeer(conn, pid, info.euid); err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	return conn, nil
}

// startListener creates the trigger file, sends the one SIGQUIT and waits
// for the socket; the trigger file is removed again either way.
func (a Attacher) startListener(ctx context.Context, pid uint32, info procInfo, dirs jvmDirs,
	sock string) error {
	if !info.quitCaught {
		return ErrNoQuitHandler
	}
	remove, err := dirs.createTrigger(".attach_pid"+strconv.FormatUint(uint64(info.nspid), 10), info.euid)
	if err != nil {
		return err
	}
	defer remove()
	if err := a.Signal(pid); err != nil {
		return fmt.Errorf("SIGQUIT: %w", err)
	}
	return waitSocket(ctx, dirs, sock)
}

// waitSocket polls for the socket until ctx ends.
func waitSocket(ctx context.Context, dirs jvmDirs, sock string) error {
	tick := time.NewTicker(socketPoll)
	defer tick.Stop()
	for {
		if _, err := dirs.statTmp(sock); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("no attach socket %s after SIGQUIT: %w", sock, ctx.Err())
		case <-tick.C:
		}
	}
}

// checkPeer verifies, from the kernel's SO_PEERCRED, that the process
// listening behind the socket is the target (pid and effective uid, both
// as the tracer's namespaces see them). A path check cannot: the target
// controls the socket's path and could swap it for a symlink to another
// process's socket — a root JVM's, say — between any check and the
// connect.
func checkPeer(conn net.Conn, pid, euid uint32) error {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return errors.New("attach socket: not a unix connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if credErr != nil {
		return fmt.Errorf("attach socket: peer credentials: %w", credErr)
	}
	if cred.Pid != int32(pid) || cred.Uid != euid {
		return fmt.Errorf("%w: pid %d uid %d, want pid %d uid %d", ErrWrongPeer,
			cred.Pid, cred.Uid, pid, euid)
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

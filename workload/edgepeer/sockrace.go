package main

// The socket races. Each one is forced into a fixed order, so the trace
// is the same in every run: a reset is sent before the step that would
// race it, and the one real race — a shutdown against a blocked accept —
// ends the same way whichever side wins. Each race prints a facts line
// (fds and ports) the integration test matches the trace against.

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"time"
)

const (
	sockScenario = "sock-races"
	// settle lets a sent RST reach its loopback peer (softirq on the
	// sending CPU, normally microseconds) before the step that must see
	// it; plenty on a loaded host.
	settle = 100 * time.Millisecond
	// raceDelay gives the accept thread time to block before the
	// shutdown; if it has not, its accept fails the same way.
	raceDelay = 200 * time.Millisecond
	// broadcastPort is any port: a connect to 255.255.255.255 fails in
	// the routing lookup, before a SYN (internal/sock_root_test.go).
	broadcastPort = 9
)

// msg is the 5-byte message the mid-stream race sends before the reset.
var msg = []byte("ping!")

// sockRaces runs every race, bracketed.
func sockRaces() error {
	return bracketed(sockScenario, func() error {
		for _, race := range []func() error{refused, unreachable, resetQueued, resetMidStream, acceptRace} {
			if err := race(); err != nil {
				return err
			}
		}
		return nil
	})
}

// refused connects to a port bound but not listening — held by this
// process, so no other process can listen on it meanwhile: the SYN gets a
// RST and connect fails with ECONNREFUSED after SYN_SENT.
func refused() error {
	holder, port, err := boundSocket()
	if err != nil {
		return err
	}
	defer closeQuietly(holder)
	fd, err := tcpSocket()
	if err != nil {
		return err
	}
	err = syscall.Connect(fd, loopback(port))
	if !errors.Is(err, syscall.ECONNREFUSED) {
		closeQuietly(fd)
		return fmt.Errorf("connect to bound port %d: %v, want ECONNREFUSED", port, err)
	}
	if err := fact("refused", "fd", fd, "port", port); err != nil {
		closeQuietly(fd)
		return err
	}
	return syscall.Close(fd)
}

// unreachable connects to the broadcast address: ENETUNREACH before any
// state transition, the path where the connect's exit must unregister
// the socket (docs/implementation.md M2 sockets, "connect").
func unreachable() error {
	fd, err := tcpSocket()
	if err != nil {
		return err
	}
	err = syscall.Connect(fd, &syscall.SockaddrInet4{Port: broadcastPort, Addr: [4]byte{255, 255, 255, 255}})
	if !errors.Is(err, syscall.ENETUNREACH) {
		closeQuietly(fd)
		return fmt.Errorf("connect to 255.255.255.255: %v, want ENETUNREACH", err)
	}
	if err := fact("unreachable", "fd", fd, "port", broadcastPort); err != nil {
		closeQuietly(fd)
		return err
	}
	return syscall.Close(fd)
}

// resetQueued resets a connection while it waits in the accept queue:
// the client connects, closes with SO_LINGER 0 (a RST, not a FIN), and
// only then does plain accept(2) take the child, already TCP_CLOSE. Its
// read fails (ECONNRESET) or sees EOF; either is reported, neither
// differs for the tracer.
func resetQueued() error {
	lfd, port, err := listener()
	if err != nil {
		return err
	}
	defer closeQuietly(lfd)
	cfd, cport, err := dial(port)
	if err != nil {
		return err
	}
	if err := resetClose(cfd); err != nil {
		return err
	}
	time.Sleep(settle)
	nfd, _, errno := syscall.Syscall(syscall.SYS_ACCEPT, uintptr(lfd), 0, 0)
	if errno != 0 {
		return fmt.Errorf("accept: %w", errno)
	}
	_, rerr := retry(func() (int, error) { return syscall.Read(int(nfd), make([]byte, 1)) })
	if err := fact("reset-queued", "client_fd", cfd, "client_port", cport,
		"server_fd", nfd, "port", port, "read_err", errName(rerr)); err != nil {
		closeQuietly(int(nfd))
		return err
	}
	return syscall.Close(int(nfd))
}

// resetMidStream resets an accepted connection after data moved: the
// server reads the client's message, the client resets, and the server's
// next read fails with ECONNRESET. Both ends are traced: one fused
// connection.
func resetMidStream() error {
	lfd, port, err := listener()
	if err != nil {
		return err
	}
	defer closeQuietly(lfd)
	cfd, cport, err := dial(port)
	if err != nil {
		return err
	}
	nfd, _, err := syscall.Accept4(lfd, syscall.SOCK_CLOEXEC)
	if err != nil {
		closeQuietly(cfd)
		return fmt.Errorf("accept4: %w", err)
	}
	defer closeQuietly(nfd)
	if err := exchangeThenReset(cfd, nfd); err != nil {
		return err
	}
	return fact("reset-mid", "client_fd", cfd, "client_port", cport, "server_fd", nfd, "port", port)
}

// exchangeThenReset moves msg from client to server, resets the client
// and checks the server's next read reports the reset.
func exchangeThenReset(cfd, nfd int) error {
	if _, err := retry(func() (int, error) { return syscall.Write(cfd, msg) }); err != nil {
		closeQuietly(cfd)
		return fmt.Errorf("write: %w", err)
	}
	buf := make([]byte, len(msg))
	if n, err := retry(func() (int, error) { return syscall.Read(nfd, buf) }); err != nil || n != len(msg) {
		closeQuietly(cfd)
		return fmt.Errorf("read %d of %d bytes: %v", n, len(msg), err)
	}
	if err := resetClose(cfd); err != nil {
		return err
	}
	time.Sleep(settle)
	_, err := retry(func() (int, error) { return syscall.Read(nfd, buf) })
	if !errors.Is(err, syscall.ECONNRESET) {
		return fmt.Errorf("read after the peer's reset: %v, want ECONNRESET", err)
	}
	return nil
}

// acceptRace shuts the listener down while another thread blocks in
// accept4 on it. close(2) alone does not wake a blocked accept on Linux
// (the thread's file reference keeps the socket open); shutdown does,
// and the accept fails with EINVAL — as it does when the shutdown wins
// the race and the accept comes second.
func acceptRace() error {
	lfd, port, err := listener()
	if err != nil {
		return err
	}
	done := make(chan acceptResult, 1)
	go blockedAccept(lfd, done)
	time.Sleep(raceDelay)
	if err := syscall.Shutdown(lfd, syscall.SHUT_RDWR); err != nil {
		closeQuietly(lfd)
		return fmt.Errorf("shutdown: %w", err)
	}
	r := <-done
	if err := syscall.Close(lfd); err != nil {
		return err
	}
	if !errors.Is(r.err, syscall.EINVAL) {
		return fmt.Errorf("accept4 racing shutdown: fd %d, %v, want EINVAL", r.fd, r.err)
	}
	return fact("accept-race", "fd", lfd, "port", port, "tid", r.tid)
}

// acceptResult is what the racing accept thread saw.
type acceptResult struct {
	fd, tid int
	err     error
}

// blockedAccept runs accept4 on a thread of its own (locked, so its tid
// is reported correctly).
func blockedAccept(lfd int, done chan<- acceptResult) {
	// The thread is never unlocked: it exits with the goroutine.
	runtime.LockOSThread()
	nfd, _, err := syscall.Accept4(lfd, syscall.SOCK_CLOEXEC)
	done <- acceptResult{fd: nfd, tid: syscall.Gettid(), err: err}
}

// tcpSocket is a blocking IPv4 TCP socket.
func tcpSocket() (int, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("socket: %w", err)
	}
	return fd, nil
}

// boundSocket is a TCP socket bound to a kernel-picked loopback port.
func boundSocket() (int, int, error) {
	fd, err := tcpSocket()
	if err != nil {
		return -1, 0, err
	}
	if err := syscall.Bind(fd, loopback(0)); err != nil {
		closeQuietly(fd)
		return -1, 0, fmt.Errorf("bind: %w", err)
	}
	port, err := localPort(fd)
	if err != nil {
		closeQuietly(fd)
		return -1, 0, err
	}
	return fd, port, nil
}

// listener is a listening loopback socket on a kernel-picked port.
func listener() (int, int, error) {
	fd, port, err := boundSocket()
	if err != nil {
		return -1, 0, err
	}
	if err := syscall.Listen(fd, 4); err != nil {
		closeQuietly(fd)
		return -1, 0, fmt.Errorf("listen: %w", err)
	}
	return fd, port, nil
}

// dial connects a new socket to the loopback port; a blocking connect
// returns once the handshake is done, the child waiting in the queue.
func dial(port int) (int, int, error) {
	fd, err := tcpSocket()
	if err != nil {
		return -1, 0, err
	}
	if err := syscall.Connect(fd, loopback(port)); err != nil {
		closeQuietly(fd)
		return -1, 0, fmt.Errorf("connect to %d: %w", port, err)
	}
	cport, err := localPort(fd)
	if err != nil {
		closeQuietly(fd)
		return -1, 0, err
	}
	return fd, cport, nil
}

// resetClose closes fd with SO_LINGER {on, 0}: the kernel sends a RST
// and drops the connection instead of the FIN handshake.
func resetClose(fd int) error {
	if err := syscall.SetsockoptLinger(fd, syscall.SOL_SOCKET, syscall.SO_LINGER,
		&syscall.Linger{Onoff: 1, Linger: 0}); err != nil {
		closeQuietly(fd)
		return fmt.Errorf("SO_LINGER: %w", err)
	}
	return syscall.Close(fd)
}

func localPort(fd int) (int, error) {
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		return 0, fmt.Errorf("getsockname: %w", err)
	}
	return sa.(*syscall.SockaddrInet4).Port, nil
}

func loopback(port int) *syscall.SockaddrInet4 {
	return &syscall.SockaddrInet4{Port: port, Addr: [4]byte{127, 0, 0, 1}}
}

// closeQuietly closes fd on a path that already fails or no longer
// cares: the first error wins.
func closeQuietly(fd int) { _ = syscall.Close(fd) }

// errName is an errno's symbolic name for the facts line, "none" for
// success.
func errName(err error) string {
	var errno syscall.Errno
	switch {
	case err == nil:
		return "none"
	case errors.As(err, &errno) && errno == syscall.ECONNRESET:
		return "ECONNRESET"
	default:
		return err.Error()
	}
}

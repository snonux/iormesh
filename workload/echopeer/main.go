// Command echopeer is the non-JVM end of the workload's non-java-peer
// scenario (PLAN.md M0 "Non-Java peer", docs/implementation.md M2 step
// 10): a TCP echo server on 127.0.0.1 that also writes a file, so the
// tracer has a process with files and sockets but no JVM — the degraded
// node shape whose events carry attribution "none".
//
// Usage: echopeer -dir D. It listens on a kernel-picked port, prints
// "listening <port>" on stdout (the Java side connects to it), echoes one
// 5-byte message, reads to the client's EOF, writes what it got to
// D/non-java-peer.log and exits 0. Exit codes: 1 failure, 2 usage.
//
// It brackets itself with the scenario's markers — failed opens of
// /iormesh-marker/non-java-peer/{begin,end}, exactly what the Java
// workload's Marker does — so the normalizer's per-process cut keeps it
// although it is a child the JVM forked. Every scenario socket and file
// syscall is a raw one from package syscall on the main thread, never
// through Go's netpoller: the declared syscalls (see
// workload/scenarios/non-java-peer/expect.json) are then exactly the ones
// below, blocking, on TID == PID. The Go runtime's own syscalls (futex,
// mmap, nanosleep on other threads) are not declared.
//
// Built cgo-free by `mage workloadBuild`. The go:debug lines and the
// thread lock follow ior's cmd/ioworkload for the same reasons: no
// cgroup reads by the runtime, and TID == PID for every scenario syscall.
//
//go:debug containermaxprocs=0
//go:debug updatemaxprocs=0
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

const (
	scenario   = "non-java-peer"
	markerRoot = "/iormesh-marker/"
	logName    = scenario + ".log"
	// msgLen is the Java side's message size (Wire.PING, "ping!").
	msgLen = 5
	// acceptTimeout ends a peer whose client never comes (the Java side
	// died), so no orphan blocks in accept forever.
	acceptTimeout = 30 * time.Second
)

// Pin the main goroutine to the main thread: the scenario's syscalls
// then all carry TID == PID, one declared thread.
func init() {
	runtime.LockOSThread()
}

func main() {
	dir := flag.String("dir", "", "directory to write "+logName+" into")
	flag.Parse()
	if *dir == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: echopeer -dir D")
		os.Exit(2)
	}
	if err := run(*dir); err != nil {
		fmt.Fprintln(os.Stderr, "echopeer:", err)
		os.Exit(1)
	}
}

// run is the bracketed scenario. A failure returns before the end
// marker, so a failed run never has a complete bracket (as in Java's
// Main.bracketed).
func run(dir string) error {
	if err := marker("begin"); err != nil {
		return err
	}
	lfd, port, err := listen()
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(os.Stdout, "listening %d\n", port); err != nil {
		_ = syscall.Close(lfd) // already failing; the first error wins
		return err
	}
	msg, err := echoOnce(lfd)
	if cerr := syscall.Close(lfd); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, logName), msg); err != nil {
		return err
	}
	return marker("end")
}

// marker opens the reserved marker path, which must not exist: the
// tracer sees the failed openat and its path, nothing is created.
func marker(edge string) error {
	path := markerRoot + scenario + "/" + edge
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err == nil {
		_ = syscall.Close(fd) // already failing; the first error wins
		return fmt.Errorf("marker path exists: %s", path)
	}
	// stderr, so stdout stays the "listening" protocol.
	fmt.Fprintln(os.Stderr, "marker "+scenario+"/"+edge)
	return nil
}

// listen binds an AF_INET socket to 127.0.0.1 on a kernel-picked port.
func listen() (int, int, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return -1, 0, fmt.Errorf("socket: %w", err)
	}
	port, err := bindLoopback(fd)
	if err != nil {
		_ = syscall.Close(fd) // already failing; the first error wins
		return -1, 0, err
	}
	return fd, port, nil
}

func bindLoopback(fd int) (int, error) {
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		return 0, fmt.Errorf("bind: %w", err)
	}
	// Backlog 1: exactly one client is expected.
	if err := syscall.Listen(fd, 1); err != nil {
		return 0, fmt.Errorf("listen: %w", err)
	}
	tv := syscall.NsecToTimeval(acceptTimeout.Nanoseconds())
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
		return 0, fmt.Errorf("setsockopt: %w", err)
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		return 0, fmt.Errorf("getsockname: %w", err)
	}
	return sa.(*syscall.SockaddrInet4).Port, nil
}

// echoOnce accepts one client, echoes its message, then reads to its
// EOF before closing: the client always closes first, so the teardown
// is the same in every run (as in the Java servers).
func echoOnce(lfd int) ([]byte, error) {
	cfd, _, err := syscall.Accept(lfd) // accept4(…, 0) on Linux
	if err != nil {
		return nil, fmt.Errorf("accept: %w", err)
	}
	msg, err := echo(cfd)
	if cerr := syscall.Close(cfd); err == nil {
		err = cerr
	}
	return msg, err
}

func echo(cfd int) ([]byte, error) {
	msg := make([]byte, msgLen)
	if err := readFull(cfd, msg); err != nil {
		return nil, err
	}
	if err := writeAll(cfd, msg); err != nil {
		return nil, err
	}
	n, err := retry(func() (int, error) { return syscall.Read(cfd, make([]byte, 1)) })
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	if n != 0 {
		return nil, errors.New("want EOF, got more data")
	}
	return msg, nil
}

// writeFile writes data to path with O_CREAT|O_TRUNC — one openat, one
// write, one close.
func writeFile(path string, data []byte) error {
	fd, err := syscall.Open(path,
		syscall.O_WRONLY|syscall.O_CREAT|syscall.O_TRUNC|syscall.O_CLOEXEC, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	werr := writeAll(fd, data)
	if cerr := syscall.Close(fd); werr == nil {
		werr = cerr
	}
	return werr
}

// readFull and writeAll loop like the Java side's Wire: 5 bytes move in
// one syscall, so the loops run once and the declared counts hold; a
// short transfer is a slower run, never a wrong one.
func readFull(fd int, buf []byte) error {
	for off := 0; off < len(buf); {
		n, err := retry(func() (int, error) { return syscall.Read(fd, buf[off:]) })
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("EOF after %d of %d bytes", off, len(buf))
		}
		off += n
	}
	return nil
}

func writeAll(fd int, buf []byte) error {
	for off := 0; off < len(buf); {
		n, err := retry(func() (int, error) { return syscall.Write(fd, buf[off:]) })
		if err != nil {
			return fmt.Errorf("write: %w", err)
		}
		off += n
	}
	return nil
}

// retry repeats a syscall interrupted before it moved data. Go installs
// its signal handlers with SA_RESTART, so EINTR should not happen; if
// it does, the extra attempt is a visible extra event, not a failure.
func retry(f func() (int, error)) (int, error) {
	for {
		n, err := f()
		if !errors.Is(err, syscall.EINTR) {
			return n, err
		}
	}
}

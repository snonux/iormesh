package main

// The port-owner role (task mh4): one incarnation of an app a
// -listen-port session follows across restarts. The test runs it twice
// on one port — kills the first, starts the second — and checks the
// session adopted the second and traced its work.

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// portOwnerScenario brackets each incarnation's file work.
const portOwnerScenario = "port-follow"

// portOwner listens on 127.0.0.1:port before it prints its PID — the
// tracer resolves the port's owner at start, so the socket must exist
// before the test starts it — then, past the startup gate, writes
// D/port-owner-<pid>.dat bracketed as port-follow, prints "worked" and
// holds the port until stdin's EOF (or the test's kill). SO_REUSEADDR
// lets the next incarnation bind at once after this one is gone.
func portOwner(port int, dir string) error {
	fd, err := listenTCP(port)
	if err != nil {
		return err
	}
	defer func() { _ = syscall.Close(fd) }()
	return gated(func() error {
		name := fmt.Sprintf("port-owner-%d.dat", os.Getpid())
		err := bracketed(portOwnerScenario, func() error {
			return writeFile(filepath.Join(dir, name), []byte("owner\n"))
		})
		if err != nil {
			return err
		}
		if err := fact("worked", "pid", os.Getpid()); err != nil {
			return err
		}
		return readToEOF(0)
	})
}

// listenTCP returns a raw socket listening on 127.0.0.1:port.
func listenTCP(port int) (int, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("socket: %w", err)
	}
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
		_ = syscall.Close(fd) // already failing
		return -1, fmt.Errorf("SO_REUSEADDR: %w", err)
	}
	addr := &syscall.SockaddrInet4{Port: port, Addr: [4]byte{127, 0, 0, 1}}
	if err := syscall.Bind(fd, addr); err != nil {
		_ = syscall.Close(fd) // already failing
		return -1, fmt.Errorf("bind 127.0.0.1:%d: %w", port, err)
	}
	if err := syscall.Listen(fd, 16); err != nil {
		_ = syscall.Close(fd) // already failing
		return -1, fmt.Errorf("listen: %w", err)
	}
	return fd, nil
}

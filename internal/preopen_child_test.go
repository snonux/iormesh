package internal_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// The child side of preopen_root_test.go (task cm4): the test binary
// re-run with preopenChildEnv set opens a loopback TCP connection to
// itself — listener, client and accepted server end, all with plain
// blocking syscalls — before anything traces it, prints a preopenInfo
// line, and waits. On the go line it moves preopenRequest bytes client
// to server and preopenResponse bytes back, prints a done line, and
// holds the sockets until its stdin closes. TestMain dispatches it.

// preopenChildEnv makes the test binary a preopen child.
const preopenChildEnv = "IORMESH_PREOPEN_CHILD"

// The exchange after the go line, in bytes each way.
const (
	preopenRequest  = 5
	preopenResponse = 7
)

// preopenInfo is what a preopen child reports once its connection is
// open: both ends' descriptors and ports. The listener stays open, as a
// server's does.
type preopenInfo struct {
	ClientFd, ServerFd     int32
	ClientPort, ServerPort uint32
}

// preopenChildMain runs the child and turns its verdict into an exit
// status.
func preopenChildMain() int {
	if err := runPreopenChild(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "preopen child:", err)
		return 1
	}
	return 0
}

// runPreopenChild opens the connection, reports it, and runs the
// exchange on the go line.
func runPreopenChild(in io.Reader, out io.Writer) error {
	ln, info, err := preopenConnect()
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(ln) }()
	defer func() { _ = unix.Close(int(info.ClientFd)) }()
	defer func() { _ = unix.Close(int(info.ServerFd)) }()
	if err := json.NewEncoder(out).Encode(info); err != nil {
		return err
	}
	r := bufio.NewReader(in)
	if _, err := r.ReadString('\n'); err != nil {
		return fmt.Errorf("wait for go: %w", err)
	}
	if err := preopenExchange(info); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "done"); err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, r) // hold the sockets until stdin closes
	return nil
}

// preopenConnect listens on an ephemeral loopback port, connects to it
// and accepts, returning the listener and the connection.
func preopenConnect() (int, preopenInfo, error) {
	var info preopenInfo
	ln, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	if err != nil {
		return -1, info, err
	}
	loopback := &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}
	if err := unix.Bind(ln, loopback); err != nil {
		return ln, info, err
	}
	if err := unix.Listen(ln, 4); err != nil {
		return ln, info, err
	}
	sa, err := unix.Getsockname(ln)
	if err != nil {
		return ln, info, err
	}
	info.ServerPort = uint32(sa.(*unix.SockaddrInet4).Port)
	client, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	if err != nil {
		return ln, info, err
	}
	info.ClientFd = int32(client)
	if err := unix.Connect(client, &unix.SockaddrInet4{Addr: loopback.Addr, Port: int(info.ServerPort)}); err != nil {
		return ln, info, err
	}
	if sa, err = unix.Getsockname(client); err != nil {
		return ln, info, err
	}
	info.ClientPort = uint32(sa.(*unix.SockaddrInet4).Port)
	server, _, err := unix.Accept(ln)
	if err != nil {
		return ln, info, err
	}
	info.ServerFd = int32(server)
	return ln, info, nil
}

// preopenExchange writes the request on the client end, reads it on the
// server end, and the response the other way.
func preopenExchange(info preopenInfo) error {
	for _, step := range []struct {
		from, to int32
		n        int
	}{{info.ClientFd, info.ServerFd, preopenRequest}, {info.ServerFd, info.ClientFd, preopenResponse}} {
		buf := make([]byte, step.n)
		if n, err := unix.Write(int(step.from), buf); err != nil || n != step.n {
			return fmt.Errorf("write %d bytes on fd %d: %d, %v", step.n, step.from, n, err)
		}
		for got := 0; got < step.n; {
			n, err := unix.Read(int(step.to), buf[got:])
			if err != nil || n == 0 {
				return fmt.Errorf("read on fd %d after %d bytes: %d, %v", step.to, got, n, err)
			}
			got += n
		}
	}
	return nil
}

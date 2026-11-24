package internal_test

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// The child side of peek_root_test.go (task ok4): a seq child scenario
// (streamseq_child_test.go) that peeks at queued stream bytes with each
// receive call that takes flags - recvfrom, recvmsg and recvmmsg, all
// with MSG_PEEK - and then consumes them with one read.

// peekScenario is the scenario's name.
const peekScenario = "peek"

// peekBytes is what the parent sends as one chunk: every peek sees all
// of it, and the read takes it whole.
const peekBytes = 12

// childPeek waits until the parent's chunk is queued, peeks at it three
// times and reads it once.
func childPeek(fd int) error {
	if err := awaitQueued(fd, peekBytes); err != nil {
		return err
	}
	if n, _, err := unix.Recvfrom(fd, make([]byte, peekBytes), unix.MSG_PEEK); err != nil ||
		n != peekBytes {
		return fmt.Errorf("recvfrom MSG_PEEK: %d bytes, want %d: %v", n, peekBytes, err)
	}
	if n, _, _, _, err := unix.RecvmsgBuffers(fd, buffers([]int{peekBytes}), nil, unix.MSG_PEEK); err != nil ||
		n != peekBytes {
		return fmt.Errorf("recvmsg MSG_PEEK: %d bytes, want %d: %v", n, peekBytes, err)
	}
	if err := mmsgCallFlags(unix.SYS_RECVMMSG, fd, []int{peekBytes}, unix.MSG_PEEK); err != nil {
		return err
	}
	if n, err := unix.Read(fd, make([]byte, peekBytes)); err != nil || n != peekBytes {
		return fmt.Errorf("read: %d bytes, want %d: %v", n, peekBytes, err)
	}
	return nil
}

// awaitQueued polls the socket's receive queue (SIOCINQ, the FIONREAD of
// a socket) until n bytes wait in it, so every peek sees the whole chunk
// however late the parent's write lands (task cl4: a fixed sleep could
// peek too early). It gives up after 10 s.
func awaitQueued(fd, n int) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		q, err := unix.IoctlGetInt(fd, unix.SIOCINQ) // FIONREAD on a socket
		if err != nil {
			return fmt.Errorf("SIOCINQ: %w", err)
		}
		if q >= n {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%d bytes queued after 10 s, want %d", q, n)
		}
		time.Sleep(time.Millisecond)
	}
}

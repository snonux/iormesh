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
	time.Sleep(60 * time.Millisecond) // the parent's chunk is queued whole
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

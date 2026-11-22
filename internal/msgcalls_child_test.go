package internal_test

import (
	"fmt"
	"net/netip"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The child side of msgcalls_root_test.go (task oh4): two more scenarios
// of the seq child (streamseq_child_test.go), which move known bytes
// through sendmsg, sendmmsg, recvmsg and recvmmsg - on the TCP connection
// (msgScenarioTCP) and between two UDP sockets of the child
// (msgScenarioUDP).

// Scenarios.
const (
	msgScenarioTCP = "msg-tcp"
	msgScenarioUDP = "msg-udp"
)

// The TCP scenario, call by call: a sendmsg of two buffers, a sendmmsg of
// msgTCPSendmmsg messages; a recvmsg of msgTCPRecvmsg bytes, a recvmmsg
// of msgTCPRecvmmsg messages, each buffer taken whole. Every buffer size
// differs, so a sum that misses a message shows.
var (
	msgTCPSendmsg  = []int{3, 4}
	msgTCPSendmmsg = []int{5, 6}
	msgTCPRecvmsg  = 8
	msgTCPRecvmmsg = []int{3, 6}
	msgTCPSends    = []int{7, 11}
	msgTCPRecvs    = []int{8, 9}
)

// The UDP scenario: one sendmmsg of msgUDPDatagrams, received by one
// recvmmsg, then one sendmsg and recvmsg of msgUDPSingle bytes.
var (
	msgUDPDatagrams = []int{10, 20, 30}
	msgUDPSingle    = 13
)

// childMsgTCP sends through sendmsg and sendmmsg, then receives through
// recvmsg and recvmmsg, pausing after every call so the parent can feed
// each receive exactly its bytes.
func childMsgTCP(fd int) error {
	if n, err := unix.SendmsgBuffers(fd, buffers(msgTCPSendmsg), nil, nil, 0); err != nil ||
		n != msgTCPSends[0] {
		return fmt.Errorf("sendmsg: %d bytes, want %d: %v", n, msgTCPSends[0], err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := mmsgCall(unix.SYS_SENDMMSG, fd, msgTCPSendmmsg); err != nil {
		return err
	}
	time.Sleep(20 * time.Millisecond)
	if n, _, _, _, err := unix.RecvmsgBuffers(fd, buffers([]int{msgTCPRecvmsg}), nil, 0); err != nil ||
		n != msgTCPRecvmsg {
		return fmt.Errorf("recvmsg: %d bytes, want %d: %v", n, msgTCPRecvmsg, err)
	}
	time.Sleep(40 * time.Millisecond) // the parent's second chunk is queued whole
	return mmsgCall(unix.SYS_RECVMMSG, fd, msgTCPRecvmmsg)
}

// childMsgUDP sends msgUDPDatagrams from one UDP socket to another with
// sendmmsg and takes them with recvmmsg, then one datagram through
// sendmsg and recvmsg. Both sockets are connected, so no call carries an
// address. It returns the two descriptors.
func childMsgUDP() ([2]int32, error) {
	send, recv, err := udpPair()
	if err != nil {
		return [2]int32{}, err
	}
	fds := [2]int32{int32(send), int32(recv)}
	if err := mmsgCall(unix.SYS_SENDMMSG, send, msgUDPDatagrams); err != nil {
		return fds, err
	}
	if err := mmsgCall(unix.SYS_RECVMMSG, recv, msgUDPDatagrams); err != nil {
		return fds, err
	}
	if n, err := unix.SendmsgBuffers(send, buffers([]int{msgUDPSingle}), nil, nil, 0); err != nil ||
		n != msgUDPSingle {
		return fds, fmt.Errorf("udp sendmsg: %d bytes, want %d: %v", n, msgUDPSingle, err)
	}
	if n, _, _, _, err := unix.RecvmsgBuffers(recv, buffers([]int{msgUDPSingle}), nil, 0); err != nil ||
		n != msgUDPSingle {
		return fds, fmt.Errorf("udp recvmsg: %d bytes, want %d: %v", n, msgUDPSingle, err)
	}
	return fds, nil
}

// udpPair returns two loopback UDP sockets connected to each other.
func udpPair() (send, recv int, err error) {
	var fds [2]int
	var addrs [2]netip.AddrPort
	for i := range fds {
		if fds[i], err = unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0); err != nil {
			return -1, -1, err
		}
		if err = unix.Bind(fds[i], &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
			return -1, -1, err
		}
		sa, err := unix.Getsockname(fds[i])
		if err != nil {
			return -1, -1, err
		}
		in := sa.(*unix.SockaddrInet4)
		addrs[i] = netip.AddrPortFrom(netip.AddrFrom4(in.Addr), uint16(in.Port))
	}
	for i := range fds {
		peer := addrs[1-i]
		if err = unix.Connect(fds[i], &unix.SockaddrInet4{Addr: peer.Addr().As4(), Port: int(peer.Port())}); err != nil {
			return -1, -1, err
		}
	}
	return fds[0], fds[1], nil
}

// buffers returns zeroed buffers of the given sizes.
func buffers(sizes []int) [][]byte {
	out := make([][]byte, len(sizes))
	for i, n := range sizes {
		out[i] = make([]byte, n)
	}
	return out
}

// mmsghdr is the kernel's struct mmsghdr on a 64-bit ABI: a msghdr and
// the message's length, padded to 64 bytes.
type mmsghdr struct {
	hdr unix.Msghdr
	len uint32
	_   [4]byte
}

// mmsgCall runs sendmmsg or recvmmsg (nr) on fd with one single-buffer
// message per size and requires every message to move exactly its size.
func mmsgCall(nr uintptr, fd int, sizes []int) error {
	msgs := make([]mmsghdr, len(sizes))
	iovs := make([]unix.Iovec, len(sizes))
	bufs := buffers(sizes)
	for i := range msgs {
		iovs[i].Base = &bufs[i][0]
		iovs[i].SetLen(sizes[i])
		msgs[i].hdr.Iov = &iovs[i]
		msgs[i].hdr.SetIovlen(1)
	}
	n, _, errno := unix.Syscall6(nr, uintptr(fd), uintptr(unsafe.Pointer(&msgs[0])),
		uintptr(len(msgs)), 0, 0, 0)
	if errno != 0 || int(n) != len(msgs) {
		return fmt.Errorf("syscall %d: %d messages, want %d: %v", nr, n, len(msgs), errno)
	}
	for i, m := range msgs {
		if int(m.len) != sizes[i] {
			return fmt.Errorf("syscall %d message %d: %d bytes, want %d", nr, i, m.len, sizes[i])
		}
	}
	return nil
}

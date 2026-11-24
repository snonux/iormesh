package internal_test

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The child side of msgcalls_root_test.go (task oh4): two more scenarios
// of the seq child (streamseq_child_test.go), which move known bytes
// through sendmsg, sendmmsg, recvmsg and recvmmsg - on the TCP connection
// (msgScenarioTCP), between two unconnected UDP sockets of the child
// (msgScenarioUDP) and along the msghdr peer paths those two leave out
// (msgScenarioPeers, task hl4).

// Scenarios.
const (
	msgScenarioTCP   = "msg-tcp"
	msgScenarioUDP   = "msg-udp"
	msgScenarioPeers = "msg-peers"
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

// The peers scenario (childMsgPeers): one sendmsg and recvmsg of
// msgPeerSingle bytes, a recvmmsg of msgPeerBatch from two senders, one
// unix sendmsg of msgPeerUnix bytes. Every size differs.
var (
	msgPeerSingle = 14
	msgPeerBatch  = []int{7, 9}
	msgPeerUnix   = 15
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
// sendmsg and recvmsg. Neither socket is connected (task nk4): every send
// names its peer in msg_name, every receive asks for the sender's there,
// as a DNS resolver or a QUIC server does - so the peer is only in the
// msghdr. It returns the two descriptors and their bound addresses.
func childMsgUDP() (fds [2]int32, addrs [2]string, err error) {
	send, recv, bound, err := udpPair()
	if err != nil {
		return fds, addrs, err
	}
	fds = [2]int32{int32(send), int32(recv)}
	addrs = [2]string{bound[0].String(), bound[1].String()}
	to := sockaddrBytes(bound[1])
	if err := mmsgCallNamed(unix.SYS_SENDMMSG, send, msgUDPDatagrams, 0, to); err != nil {
		return fds, addrs, err
	}
	from := make([]byte, unix.SizeofSockaddrInet4)
	if err := mmsgCallNamed(unix.SYS_RECVMMSG, recv, msgUDPDatagrams, 0, from); err != nil {
		return fds, addrs, err
	}
	dst := &unix.SockaddrInet4{Addr: bound[1].Addr().As4(), Port: int(bound[1].Port())}
	if n, err := unix.SendmsgBuffers(send, buffers([]int{msgUDPSingle}), nil, dst, 0); err != nil ||
		n != msgUDPSingle {
		return fds, addrs, fmt.Errorf("udp sendmsg: %d bytes, want %d: %v", n, msgUDPSingle, err)
	}
	n, _, _, sa, err := unix.RecvmsgBuffers(recv, buffers([]int{msgUDPSingle}), nil, 0)
	if err != nil || n != msgUDPSingle {
		return fds, addrs, fmt.Errorf("udp recvmsg: %d bytes, want %d: %v", n, msgUDPSingle, err)
	}
	if in, ok := sa.(*unix.SockaddrInet4); !ok || in.Port != int(bound[0].Port()) {
		return fds, addrs, fmt.Errorf("udp recvmsg from %+v, want %s", sa, bound[0])
	}
	return fds, addrs, nil
}

// msgPeers is what the peers scenario reports: its UDP sockets - the
// sender connected to the receiver, the other, unconnected sender - and
// the AF_UNIX datagram sender, with the UDP sockets' bound addresses and
// the unix receiver's sun_path.
type msgPeers struct {
	Send, Recv, Other, Unix       int32
	SendAddr, RecvAddr, OtherAddr string
	UnixPath                      string
}

// childMsgPeers runs the msghdr peer paths TestMsgCallsUDP leaves out
// (task hl4): a connected UDP sendmsg and an unconnected recvmsg, both
// with msg_name NULL; a recvmmsg of two datagrams from two senders; an
// AF_UNIX SOCK_DGRAM sendmsg to a sun_path.
func childMsgPeers() (*msgPeers, error) {
	r := &msgPeers{}
	if err := childMsgPeersUDP(r); err != nil {
		return nil, err
	}
	return r, childMsgPeersUnix(r)
}

// childMsgPeersUDP connects the sender to the receiver and sends one
// datagram through sendmsg with msg_name NULL, so its peer is only the
// socket's remote end; the unconnected receiver takes it through recvmsg
// with msg_name NULL, so it learns no sender at all. Then the other
// socket and the sender send one datagram each, in that order, and one
// recvmmsg takes both, asking for each sender's address: the call's
// whole byte sum goes to its first message's peer, the other socket.
func childMsgPeersUDP(r *msgPeers) error {
	send, recv, bound, err := udpPair()
	if err != nil {
		return err
	}
	other, otherAddr, err := udpBound()
	if err != nil {
		return err
	}
	r.Send, r.Recv, r.Other = int32(send), int32(recv), int32(other)
	r.SendAddr, r.RecvAddr, r.OtherAddr = bound[0].String(), bound[1].String(), otherAddr.String()
	to := &unix.SockaddrInet4{Addr: bound[1].Addr().As4(), Port: int(bound[1].Port())}
	if err := unix.Connect(send, to); err != nil {
		return err
	}
	if err := msgCallNamed(unix.SYS_SENDMSG, send, msgPeerSingle, nil); err != nil {
		return err
	}
	if err := msgCallNamed(unix.SYS_RECVMSG, recv, msgPeerSingle, nil); err != nil {
		return err
	}
	if err := unix.Sendto(other, make([]byte, msgPeerBatch[0]), 0, to); err != nil {
		return err
	}
	if _, err := unix.Write(send, make([]byte, msgPeerBatch[1])); err != nil {
		return err
	}
	from := make([]byte, unix.SizeofSockaddrInet4)
	return mmsgCallNamed(unix.SYS_RECVMMSG, recv, msgPeerBatch, 0, from)
}

// childMsgPeersUnix binds an AF_UNIX datagram socket to a file name and
// sends it msgPeerUnix bytes from an unbound one through sendmsg naming
// that sun_path, as a journald client does. The name lives under /tmp, not
// $TMPDIR, which may be too long for a sun_path.
func childMsgPeersUnix(r *msgPeers) error {
	dir, err := os.MkdirTemp("/tmp", "iormesh-msg")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	r.UnixPath = filepath.Join(dir, "sock")
	var fds [2]int
	for i := range fds {
		if fds[i], err = unix.Socket(unix.AF_UNIX, unix.SOCK_DGRAM, 0); err != nil {
			return err
		}
	}
	dst := &unix.SockaddrUnix{Name: r.UnixPath}
	if err := unix.Bind(fds[1], dst); err != nil {
		return err
	}
	r.Unix = int32(fds[0])
	if n, err := unix.SendmsgBuffers(fds[0], buffers([]int{msgPeerUnix}), nil, dst, 0); err != nil ||
		n != msgPeerUnix {
		return fmt.Errorf("unix sendmsg: %d bytes, want %d: %v", n, msgPeerUnix, err)
	}
	_, err = unix.Read(fds[1], make([]byte, msgPeerUnix))
	return err
}

// msgCallNamed runs sendmsg or recvmsg (nr) on fd with one buffer of size
// bytes and msg_name name - NULL when name is nil, which x/sys/unix's
// RecvmsgBuffers never passes - and requires it to move exactly size.
func msgCallNamed(nr uintptr, fd, size int, name []byte) error {
	buf := make([]byte, size)
	iov := unix.Iovec{Base: &buf[0]}
	iov.SetLen(size)
	hdr := unix.Msghdr{Iov: &iov}
	hdr.SetIovlen(1)
	if name != nil {
		hdr.Name, hdr.Namelen = &name[0], uint32(len(name))
	}
	n, _, errno := unix.Syscall(nr, uintptr(fd), uintptr(unsafe.Pointer(&hdr)), 0)
	if errno != 0 || int(n) != size {
		return fmt.Errorf("syscall %d: %d bytes, want %d: %v", nr, n, size, errno)
	}
	return nil
}

// udpPair returns two loopback UDP sockets, bound but not connected, and
// their addresses.
func udpPair() (send, recv int, addrs [2]netip.AddrPort, err error) {
	var fds [2]int
	for i := range fds {
		if fds[i], addrs[i], err = udpBound(); err != nil {
			return -1, -1, addrs, err
		}
	}
	return fds[0], fds[1], addrs, nil
}

// udpBound returns a UDP socket bound to a loopback port the kernel picks,
// and that address.
func udpBound() (int, netip.AddrPort, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return -1, netip.AddrPort{}, err
	}
	if err = unix.Bind(fd, &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		return -1, netip.AddrPort{}, err
	}
	sa, err := unix.Getsockname(fd)
	if err != nil {
		return -1, netip.AddrPort{}, err
	}
	in := sa.(*unix.SockaddrInet4)
	return fd, netip.AddrPortFrom(netip.AddrFrom4(in.Addr), uint16(in.Port)), nil
}

// sockaddrBytes is ap as the kernel's struct sockaddr_in, port in network
// order: the msg_name of a sendmmsg message.
func sockaddrBytes(ap netip.AddrPort) []byte {
	raw := unix.RawSockaddrInet4{Family: unix.AF_INET, Addr: ap.Addr().As4()}
	port := (*[2]byte)(unsafe.Pointer(&raw.Port))
	port[0], port[1] = byte(ap.Port()>>8), byte(ap.Port())
	return (*[unix.SizeofSockaddrInet4]byte)(unsafe.Pointer(&raw))[:]
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
	return mmsgCallFlags(nr, fd, sizes, 0)
}

// mmsgCallFlags is mmsgCall with the call's flags argument (MSG_PEEK for
// the peek scenario, peek_child_test.go).
func mmsgCallFlags(nr uintptr, fd int, sizes []int, flags int) error {
	return mmsgCallNamed(nr, fd, sizes, flags, nil)
}

// mmsgCallNamed is mmsgCallFlags with a msg_name for every message (none
// when name is nil): a copy of name each - the peer a sendmmsg sends to,
// or the buffer a recvmmsg writes each sender's address into.
func mmsgCallNamed(nr uintptr, fd int, sizes []int, flags int, name []byte) error {
	msgs := make([]mmsghdr, len(sizes))
	iovs := make([]unix.Iovec, len(sizes))
	bufs := buffers(sizes)
	for i := range msgs {
		iovs[i].Base = &bufs[i][0]
		iovs[i].SetLen(sizes[i])
		msgs[i].hdr.Iov = &iovs[i]
		msgs[i].hdr.SetIovlen(1)
		if name != nil {
			own := append([]byte(nil), name...)
			msgs[i].hdr.Name, msgs[i].hdr.Namelen = &own[0], uint32(len(own))
		}
	}
	n, _, errno := unix.Syscall6(nr, uintptr(fd), uintptr(unsafe.Pointer(&msgs[0])),
		uintptr(len(msgs)), uintptr(flags), 0, 0)
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

package internal_test

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"iormesh/internal/federate"
	"iormesh/internal/schema"
)

// Connections open before the session (decision #35, task cm4), from the
// real kernel: a child (preopen_child_test.go) opens a loopback
// connection to itself before the tracer loads, the tracer seeds the
// child's sockets (bpf.Tracer.SeedSockets, sock.c's two task-file
// iterators), and the child then moves bytes both ways. Root only;
// listed in gatecmd.RootBPFTests.

// preopenChild is a running preopen child.
type preopenChild struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	info   preopenInfo
}

// startPreopenChild starts a child and waits until its connection is
// open.
func startPreopenChild(t *testing.T) *preopenChild {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), preopenChildEnv+"=1")
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	c := &preopenChild{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
	t.Cleanup(c.stop)
	line, err := c.stdout.ReadBytes('\n')
	if err != nil {
		t.Fatalf("child info: %v", err)
	}
	if err := json.Unmarshal(line, &c.info); err != nil {
		t.Fatalf("child info %q: %v", line, err)
	}
	return c
}

func (c *preopenChild) pid() uint32 { return uint32(c.cmd.Process.Pid) }

// exchange lets the child move its bytes and waits until it did.
func (c *preopenChild) exchange(t *testing.T) {
	t.Helper()
	if _, err := io.WriteString(c.stdin, "go\n"); err != nil {
		t.Fatalf("go: %v", err)
	}
	if line, err := c.stdout.ReadString('\n'); err != nil || line != "done\n" {
		t.Fatalf("child exchange: %q, %v", line, err)
	}
}

func (c *preopenChild) stop() {
	_ = c.stdin.Close()
	if c.cmd.ProcessState == nil {
		done := make(chan struct{})
		go func() { _ = c.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = c.cmd.Process.Kill()
			<-done
		}
	}
}

// TestSeedPreopenedConnection: a connection the target opened before the
// session gets one initial tcp_state event per end — initial_client on
// the connecting end, initial_server on the accepted one, whose port a
// listener holds — with the tuple and the four tcp_sock fields; the
// reads and writes on both ends carry one conn id; split into two hosts,
// the ends rebuild one ISN pair and the merge fuses them. Seeding twice
// adds nothing, and a bystander with a connection of its own, opened the
// same way but not a target, gets no record.
func TestSeedPreopenedConnection(t *testing.T) {
	requireRoot(t)
	child := startPreopenChild(t)
	bystander := startPreopenChild(t)
	tr := openSeqTracer(t, child.pid(), false)
	log := collectRecords(tr)
	for range 2 {
		if err := tr.SeedSockets(); err != nil {
			t.Fatalf("seed sockets: %v", err)
		}
	}
	child.exchange(t)
	bystander.exchange(t)
	evs := log.all(t)
	for _, e := range evs {
		if e.PID == bystander.pid() {
			t.Errorf("bystander event %s on fd %d", e.Event, e.FD)
		}
	}
	info := child.info
	client := checkInitial(t, evs, child.pid(), info.ClientFd, schema.SockSnapshotInitialClient,
		info.ClientPort, info.ServerPort)
	server := checkInitial(t, evs, child.pid(), info.ServerFd, schema.SockSnapshotInitialServer,
		info.ServerPort, info.ClientPort)
	if client.ConnID == "" || client.ConnID != server.ConnID {
		t.Errorf("conn ids: client %q, server %q; want one, fused on this host", client.ConnID, server.ConnID)
	}
	checkDataConn(t, evs, child.pid(), info.ClientFd, schema.KindWrite, client.ConnID)
	checkDataConn(t, evs, child.pid(), info.ServerFd, schema.KindRead, client.ConnID)
	checkDataConn(t, evs, child.pid(), info.ServerFd, schema.KindWrite, client.ConnID)
	checkDataConn(t, evs, child.pid(), info.ClientFd, schema.KindRead, client.ConnID)
	checkSplitFuses(t, evs, child.pid(), info)
}

// checkInitial requires exactly one initial tcp_state event of pid's fd,
// marked want, ESTABLISHED, with the tuple local -> remote over
// 127.0.0.1 and the four tcp_sock fields, and returns it.
func checkInitial(t *testing.T, evs []schema.Event, pid uint32, fd int32, want string,
	local, remote uint32) schema.Event {
	t.Helper()
	var found []schema.Event
	for _, e := range eventsOf(evs, pid, fd) {
		if e.Event == schema.KindTCPState && e.Args.SockSnapshot != nil {
			found = append(found, e)
		}
	}
	if len(found) != 1 {
		t.Fatalf("fd %d: %d initial tcp_state events, want 1 (%s)", fd, len(found), want)
	}
	e := found[0]
	a := e.Args
	switch {
	case *a.SockSnapshot != want:
		t.Errorf("fd %d: sock_snapshot %q, want %q", fd, *a.SockSnapshot, want)
	case a.SockState == nil || *a.SockState != "ESTABLISHED":
		t.Errorf("fd %d: sock_state %v, want ESTABLISHED", fd, a.SockState)
	case a.LocalPort == nil || *a.LocalPort != uint16(local) || a.RemotePort == nil ||
		*a.RemotePort != uint16(remote) || *a.LocalAddr != "127.0.0.1" || *a.RemoteAddr != "127.0.0.1":
		t.Errorf("fd %d: tuple %v:%v -> %v:%v, want 127.0.0.1:%d -> 127.0.0.1:%d", fd,
			a.LocalAddr, a.LocalPort, a.RemoteAddr, a.RemotePort, local, remote)
	case a.SndUna == nil:
		t.Errorf("fd %d: no tcp_sock fields", fd)
	}
	return e
}

// checkDataConn requires a kind event of pid's fd with conn id want.
func checkDataConn(t *testing.T, evs []schema.Event, pid uint32, fd int32, kind, want string) {
	t.Helper()
	for _, e := range eventsOf(evs, pid, fd) {
		if e.Event != kind {
			continue
		}
		if e.ConnID != want {
			t.Errorf("fd %d: %s with conn id %q, want %q", fd, kind, e.ConnID, want)
		}
		return
	}
	t.Errorf("fd %d: no %s event", fd, kind)
}

// checkSplitFuses puts each end's events on a host of its own, as if the
// two ends had been traced on two machines, and requires the merge to
// find a client and a server half with one key and fuse them.
func checkSplitFuses(t *testing.T, evs []schema.Event, pid uint32, info preopenInfo) {
	t.Helper()
	var split []schema.Event
	for host, fd := range map[string]int32{"client-host": info.ClientFd, "server-host": info.ServerFd} {
		for _, e := range eventsOf(evs, pid, fd) {
			e.HostID = host
			split = append(split, e)
		}
	}
	halves := federate.Halves(split)
	if len(halves) != 2 {
		t.Fatalf("%d halves, want 2: %+v", len(halves), halves)
	}
	if halves[0].Role != federate.RoleClient || halves[1].Role != federate.RoleServer {
		t.Errorf("roles %q, %q; want client, server", halves[0].Role, halves[1].Role)
	}
	if !halves[0].HasKey || !halves[1].HasKey || halves[0].Key != halves[1].Key {
		t.Errorf("keys %+v (%v), %+v (%v); want one ISN pair", halves[0].Key, halves[0].HasKey,
			halves[1].Key, halves[1].HasKey)
	}
	if f := federate.Fuse(halves, federate.DefaultWindow); len(f.Conns) != 1 {
		t.Errorf("fused %d connections, unresolved %+v; want 1", len(f.Conns), f.Unresolved)
	}
}

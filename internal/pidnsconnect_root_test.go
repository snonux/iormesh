package internal_test

// Root test of a TCP connect traced in pod mode (M7, task 4f4) - the
// connect_sock_map key the state tracepoint looks up (task if4). The test
// binary re-runs itself in a fresh PID namespace as the tracer, and
// targets a bash there that connects to it. Listed in
// gatecmd.RootBPFTests; `mage bpfRootTest` runs it.

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	ibpf "iormesh/internal/bpf"
	"iormesh/internal/types"
)

// pidnsConnectEnv makes the test binary the namespace side of
// TestPidNSConnectEstablished (TestMain).
const pidnsConnectEnv = "IORMESH_PIDNS_CONNECT_CHILD"

// connectScript connects to 127.0.0.1:$1 with bash's /dev/tcp once a line
// arrives on its stdin, says so, and holds the connection until stdin
// closes. A process of its own: the tracer never traces itself
// (IOR_PID_FILTER).
const connectScript = `read -r _ && exec 3<>/dev/tcp/127.0.0.1/$1 && echo ok && read -r _`

// TestPidNSConnectEstablished: a tracer in its own PID namespace reports a
// loopback connect of a target there (a bash beside it) with its
// SYN_SENT -> ESTABLISHED record, under the namespace's pid. The
// stale-owner check (sock.c, iormesh_sock_owner_stale) lets a
// CLOSE -> SYN_SENT through only for the connect parked in
// connect_sock_map, which the connect's enter keys by the namespace tid;
// looked up by the host tid it judged every pod-mode connect stale,
// deleted the entry and dropped all its state records.
func TestPidNSConnectEstablished(t *testing.T) {
	requireRoot(t)
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("needs unshare(1)")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("unshare", "--pid", "--fork", "--mount-proc", self)
	cmd.Env = append(os.Environ(), pidnsConnectEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("namespace tracer: %v\n%s", err, out)
	}
	t.Logf("namespace tracer: %s", bytes.TrimSpace(out))
}

// pidnsConnectMain runs the namespace side and turns its verdict into an
// exit status.
func pidnsConnectMain() int {
	if err := runPidNSConnect(); err != nil {
		fmt.Fprintln(os.Stderr, "pidns connect:", err)
		return 1
	}
	return 0
}

// runPidNSConnect opens the tracer, starts connectScript and targets it by
// its pid in the namespace, lets it connect to a listener here and waits
// for its SYN_SENT -> ESTABLISHED record.
func runPidNSConnect() error {
	tr, err := ibpf.Open(ibpf.Config{})
	if err != nil {
		return fmt.Errorf("open tracer: %w", err)
	}
	defer func() { _ = tr.Close() }()
	if tr.PidNS() == 0 {
		return errors.New("the tracer reports the initial PID namespace")
	}
	ln, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return err
	}
	defer func() { _ = ln.Close() }()
	port := uint32(ln.Addr().(*net.TCPAddr).Port)
	client, err := startNSWorkload("bash", "-c", connectScript, "connect", fmt.Sprint(port))
	if err != nil {
		return err
	}
	defer client.stop()
	pid := uint32(client.cmd.Process.Pid)
	if err := tr.AddTarget(pid); err != nil {
		return fmt.Errorf("add target %d: %w", pid, err)
	}
	if ack, err := client.poke(); err != nil || ack != "ok" {
		return fmt.Errorf("connect script: %q, %v", ack, err)
	}
	ev := waitEstablished(tr, port, 5*time.Second)
	if ev == nil {
		return fmt.Errorf("no SYN_SENT -> ESTABLISHED record of pid %d to port %d", pid, port)
	}
	if ev.Pid != pid {
		return fmt.Errorf("record to port %d under pid %d, want the namespace pid %d", port, ev.Pid, pid)
	}
	fmt.Printf("ok: pidns %d, pid %d, :%d -> :%d established\n", tr.PidNS(), pid, ev.Sport, port)
	return nil
}

// waitEstablished returns the SYN_SENT -> ESTABLISHED record of the
// connection to remote port port, or nil after wait.
func waitEstablished(tr *ibpf.Tracer, port uint32, wait time.Duration) *types.TcpStateEvent {
	const synSent, established = 2, 1
	deadline := time.After(wait)
	for {
		select {
		case batch, ok := <-tr.Records():
			if !ok {
				return nil
			}
			var found *types.TcpStateEvent
			for _, rec := range batch {
				ev, isState := rec.(*types.TcpStateEvent)
				if isState && ev.Oldstate == synSent && ev.Newstate == established && ev.Dport == port {
					copied := *ev
					found = &copied
				}
			}
			tr.Release(batch)
			if found != nil {
				return found
			}
		case <-deadline:
			return nil
		}
	}
}

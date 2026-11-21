package integrationtests

// M2 step 7 end to end: local IPC, signals, UDP and both TCP address
// families under the real tracer pass their goldens (PLAN.md M2 "Full
// mental-model scope"; docs/implementation.md M2 step 7). pipe-ipc and
// signals need their opt-in families (decision #7: -trace-families ipc,
// signal); unix-socket, udp-loopback and tcp-ipv4/6 run on the defaults,
// since unix sockets and UDP are the socket family.

import (
	"path/filepath"
	"slices"
	"testing"

	"iormesh/internal/normalize"
	"iormesh/internal/schema"
)

// TestPipeIPC: pipe2 (pipe with an older libc, pipeKindOfThisLibc) names
// both ends of each pipe, the child JVM's dup2s
// put the stdin and stdout pipes on its fd 0 and 1, and every pipe is one
// fused connection — the in-process one of the parent alone, the stdio
// ones of parent and child.
func TestPipeIPC(t *testing.T) {
	got, want := ipcScenario(t, "pipe-ipc", "ipc", optionless("pipe-ipc"))
	t.Run("wrong expectations are caught", func(t *testing.T) {
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			"pipe2's ends swapped": func(x *normalize.Expectation) {
				x.Events = slices.Clone(x.Events)
				x.Events[0].FD, x.Events[0].Args.FD2 = x.Events[0].Args.FD2, x.Events[0].FD
			},
			"the child holds the in-process pipe": func(x *normalize.Expectation) {
				x.Edges = append(slices.Clone(x.Edges), normalize.ExpectedEdge{
					Type: schema.EdgeConnected, From: "$p2", To: "conn:$c1", State: schema.StateFused})
			},
		})
	})
}

// TestSignals: each kill names its signal (SIGTERM, then SIGKILL), each
// child receives it as a signal event, and signaled edges link the
// parent to both children.
func TestSignals(t *testing.T) {
	got, want := ipcScenario(t, "signals", "signal", optionless("signals"))
	t.Run("wrong expectations are caught", func(t *testing.T) {
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			"SIGKILL first": func(x *normalize.Expectation) {
				x.Events = slices.Clone(x.Events)
				for i, e := range x.Events {
					if e.Event == schema.KindKill {
						sig := *e.Args.Signal ^ 15 ^ 9 // swap 15 and 9
						x.Events[i].Args.Signal = &sig
					}
				}
			},
			"a child signalled its parent": func(x *normalize.Expectation) {
				x.Edges = append(slices.Clone(x.Edges), normalize.ExpectedEdge{
					Type: schema.EdgeSignaled, From: "$p2", To: "$p1"})
			},
		})
	})
}

// TestUnixSocket: bind and connect name the socket file, and the
// accepted end and the client's end fuse into one connection.
func TestUnixSocket(t *testing.T) {
	got, want := ipcScenario(t, "unix-socket", "", func(h *Harness) ([]*Workload, error) {
		server, err := h.StartWorkload("unix-socket", "--role", "server")
		if err != nil {
			return nil, err
		}
		server.ReleasedWhen = "listening "
		path := filepath.Join(h.Tmp, "unix-socket.sock")
		client, err := h.StartNoDir("unix-socket", "--role", "client", "--path", path)
		if err != nil {
			return nil, err
		}
		return []*Workload{server, client}, nil
	})
	t.Run("wrong expectations are caught", func(t *testing.T) {
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			"two connections": func(x *normalize.Expectation) {
				x.Edges = slices.Clone(x.Edges)
				x.Edges[1].To = "conn:$c2"
			},
		})
	})
}

// TestUDPLoopback: every datagram carries its local and remote address,
// the wildcard-bound client only its port, and the exchange is one fused
// flow.
func TestUDPLoopback(t *testing.T) {
	got, want := ipcScenario(t, "udp-loopback", "", func(h *Harness) ([]*Workload, error) {
		port := freePort(t)
		server, err := h.StartNoDir("udp-loopback", "--role", "server", "--port", port)
		if err != nil {
			return nil, err
		}
		server.ReleasedWhen = "listening "
		client, err := h.StartNoDir("udp-loopback", "--role", "client", "--port", port)
		if err != nil {
			return nil, err
		}
		return []*Workload{server, client}, nil
	})
	t.Run("wrong expectations are caught", func(t *testing.T) {
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			"the client's datagram from the server's port": func(x *normalize.Expectation) {
				x.Events = slices.Clone(x.Events)
				for i, e := range x.Events {
					if e.Process == "$p2" && e.Event == schema.KindSendto {
						x.Events[i].Args.LocalPort = x.Events[i].Args.RemotePort
					}
				}
			},
		})
	})
}

// TestTCPFamilies: one AF_INET and one AF_INET6 connection, each with its
// full 4-tuple, fused, and both ends' tcp_state records carrying the
// tcp_sock fields from which internal/federate rebuilds one ISN pair (M4,
// task ve4; assertISNKeysMatch), and its sends and receives carrying
// tcp_seq, from which the merge bounds the two pretend hosts' clock
// offset from both sides (task xe4; assertClockBounds). tcp-ipv6 skips on
// a host without an IPv6 loopback (the workload's exit 3).
func TestTCPFamilies(t *testing.T) {
	for _, scenario := range []string{"tcp-ipv4", "tcp-ipv6"} {
		t.Run(scenario, func(t *testing.T) {
			got, want, run := ipcScenarioRun(t, scenario, "", optionless(scenario))
			assertISNKeysMatch(t, run.Trace.Events)
			assertClockBounds(t, run.Trace.Events)
			assertCaught(t, got, want, map[string]func(*normalize.Expectation){
				"the other family's loopback": func(x *normalize.Expectation) {
					other := "::1"
					if scenario == "tcp-ipv6" {
						other = "127.0.0.1"
					}
					x.Events = slices.Clone(x.Events)
					x.Events[0].Args.LocalAddr = &other
				},
			})
		})
	}
}

// ipcScenario traces one step 7 scenario with the given opt-in families
// and asserts it passes its golden and that only its processes (and the
// children they forked) were traced.
func ipcScenario(t *testing.T, scenario, families string, start starter) (
	normalize.Trace, normalize.Expectation) {
	t.Helper()
	got, want, _ := ipcScenarioRun(t, scenario, families, start)
	return got, want
}

// ipcScenarioRun is ipcScenario that also returns the raw run, for
// checks on what normalization drops (the tcp_sock sequence fields).
func ipcScenarioRun(t *testing.T, scenario, families string, start starter) (
	normalize.Trace, normalize.Expectation, tracedRun) {
	t.Helper()
	enableParallelIfRequested(t)
	h := newHarness(t)
	h.Families = families
	run := traceJudged(t, h, start)
	assertTracedOrForked(t, run.Trace, run.pids)
	got := normalized(t, h, run, scenario)
	want := pipeKindOfThisLibc(t, got, expectation(t, scenario))
	assertGolden(t, got, want)
	return got, want, run
}

// pipeKindOfThisLibc returns want with its declared pipe2 events declared
// as pipe when the trace holds pipe and no pipe2 (task nf4). Java's
// pipe() call is the libc's choice of syscall: glibc 2.34 (Rocky 9) issues
// pipe(2) on x86_64, a newer one (Fedora's 2.43) pipe2(fds, 0). The tracer
// reports the syscall the process made, and both carry the same fds, so
// the golden holds either way; only the kind's name follows the libc.
func pipeKindOfThisLibc(t *testing.T, got normalize.Trace, want normalize.Expectation) normalize.Expectation {
	t.Helper()
	kinds := map[string]bool{}
	for _, e := range got.Events {
		kinds[e.Event] = true
	}
	if kinds[schema.KindPipe2] || !kinds[schema.KindPipe] {
		return want
	}
	want.Events = slices.Clone(want.Events)
	for i, e := range want.Events {
		if e.Event == schema.KindPipe2 {
			want.Events[i].Event = schema.KindPipe
		}
	}
	t.Logf("golden %s: the libc issues pipe, not pipe2; pipe2 declared as pipe", want.Scenario)
	return want
}

// optionless starts a solo scenario that takes no options (pipe-ipc,
// signals, tcp-ipv4, tcp-ipv6).
func optionless(scenario string) starter {
	return func(h *Harness) ([]*Workload, error) {
		w, err := h.StartOptionless(scenario)
		if err != nil {
			return nil, err
		}
		return []*Workload{w}, nil
	}
}

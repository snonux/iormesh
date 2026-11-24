package correlate

import (
	"encoding/json"
	"testing"

	"golang.org/x/sys/unix"

	"iormesh/internal/federate"
	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// execBoth feeds an exec the way the tracer reports one: the
// sched_process_exec record, then the exec event (Exec) once the argv is
// read.
func (f feeder) execBoth(comm string) {
	f.execRecord(comm)
	if err := f.c.Exec(ExecInfo{PID: f.pid, TID: f.tid, BootNS: f.tick(), Exe: "/bin/" + comm}); err != nil {
		f.t.Fatal(err)
	}
}

// rebuildCase drives a correlator through parent (pid 10) and child
// (pid 20) feeders sharing one clock.
type rebuildCase struct {
	name string
	run  func(parent, child feeder)
}

// TestRebuiltGraphIsTheCorrelators (task ze4, decision #3): the graph the
// controller rebuilds from the events (federate.RebuildGraph) is the
// correlator's own, for the graph state the fork and attach events and
// F_SETFD's flags carry: forked edges, a child's inherited fds,
// close-on-exec set by fcntl, app nodes and incarnation edges, a target
// that never made a traced call, and a pid reused within the session.
func TestRebuiltGraphIsTheCorrelators(t *testing.T) {
	for _, tc := range append(fdRebuildCases(), lifecycleRebuildCases()...) {
		t.Run(tc.name, func(t *testing.T) {
			c, sink := newTest(t)
			now := new(uint64)
			tc.run(feeder{t, c, 10, 11, now}, feeder{t, c, 20, 20, now})
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			want, got := graphJSON(t, c.Graph()), graphJSON(t, federate.RebuildGraph(sink.events))
			if got != want {
				t.Errorf("rebuilt\n%s\nthe correlator's\n%s", got, want)
			}
		})
	}
}

// fdRebuildCases: the fd table through forks, fcntl, execs and dups,
// and the connections of pipes and unix sockets.
func fdRebuildCases() []rebuildCase {
	return []rebuildCase{
		{"a child's rename moves the fd it inherited", func(p, c feeder) {
			p.open("/d/a.tmp", 0, 4)
			p.fork(20)
			p.close(4)
			c.rename("/d/a.tmp", "/d/a")
		}},
		{"an F_SETFD close-on-exec fd is gone after the child's exec", func(p, c feeder) {
			p.open("/d/x", 0, 4)
			p.fcntl(4, unix.F_SETFD, unix.FD_CLOEXEC, 0)
			p.fork(20)
			c.execBoth("sh")
			p.rename("/d/x", "/d/y")
		}},
		{"F_SETFD clearing close-on-exec keeps the fd", func(p, c feeder) {
			p.open("/d/x", unix.O_CLOEXEC, 4)
			p.fcntl(4, unix.F_SETFD, 0, 0)
			p.fork(20)
			c.execBoth("sh")
			c.rename("/d/x", "/d/y")
		}},
		{"F_GETFD reveals close-on-exec", func(p, _ feeder) {
			p.open("/d/x", 0, 4)
			p.fcntl(4, unix.F_GETFD, 0, unix.FD_CLOEXEC)
			p.execBoth("java")
			p.rename("/d/x", "/d/y")
		}},
		{"dup2 onto itself keeps the path", func(p, _ feeder) {
			p.open("/d/x", 0, 4)
			p.fdCall(types.SYS_ENTER_DUP2, types.SYS_EXIT_DUP2, 4, 4)
			p.rename("/d/x", "/d/y")
		}},
		{"a pipe shared with a forked child", func(p, c feeder) {
			p.pipe2(0, 5, 6)
			p.fork(20)
			c.read(5, 1)
			p.write(6, 1)
		}},
		{"two unix accepts of reused sockets", reusedUnixAccepts},
	}
}

// lifecycleRebuildCases: attaches, and a pid reused by a fork or an
// attach after its first process leaked an open fd — a later rename must
// not move the dead process's fd (the reviewer's repro).
func lifecycleRebuildCases() []rebuildCase {
	reused := Identity{Comm: "again", StartNS: 7000}
	leak := func(p, c feeder) {
		p.fork(20)
		c.open("/d/x", 0, 4) // never closed: pid 20 exits holding it
		c.c.cfg.ProcInfo = func(pid uint32) (string, uint64) { return "again", reused.StartNS }
	}
	return []rebuildCase{
		{"a target that never calls", func(p, _ feeder) {
			if err := p.c.Seed(30, testIdentity(30)); err != nil {
				p.t.Fatal(err)
			}
			p.open("/d/x", 0, 4)
		}},
		{"pattern incarnations under one app", func(p, _ feeder) {
			for i, pid := range []uint32{40, 41} {
				if err := p.c.Incarnation("*app*", pid, testIdentity(pid), uint64(i)); err != nil {
					p.t.Fatal(err)
				}
			}
			p.open("/d/x", 0, 4)
		}},
		{"a pid reused by a fork", func(p, c feeder) {
			leak(p, c)
			p.fork(20)
			p.rename("/d/x", "/d/y")
		}},
		{"a pid reused by an attach", func(p, c feeder) {
			leak(p, c)
			if err := p.c.Incarnation("*app*", 20, reused, 0); err != nil {
				p.t.Fatal(err)
			}
			p.rename("/d/x", "/d/y")
		}},
	}
}

func graphJSON(t *testing.T, g schema.Graph) string {
	t.Helper()
	b, err := json.MarshalIndent(g, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

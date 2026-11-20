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

// TestRebuiltGraphIsTheCorrelators (task ze4, decision #3): the graph the
// controller rebuilds from the events (federate.RebuildGraph) is the
// correlator's own, for the graph state the fork and attach events and
// F_SETFD's flags carry: forked edges, a child's inherited fds,
// close-on-exec set by fcntl, app nodes and incarnation edges, and a
// target that never made a traced call.
func TestRebuiltGraphIsTheCorrelators(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(parent, child feeder)
	}{
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
		{"F_GETFD reveals close-on-exec", func(p, c feeder) {
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
	} {
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

func graphJSON(t *testing.T, g schema.Graph) string {
	t.Helper()
	b, err := json.MarshalIndent(g, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

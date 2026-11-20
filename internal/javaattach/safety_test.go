package javaattach

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The confused-deputy cases (task ge4's security review): the target
// controls every path of the handshake, and the tracer is root.

// movedTo renames the fake process's directory to pid: a target whose
// pid is not the test process, which plays every listener.
func (p fakeProc) movedTo(t *testing.T, pid int) fakeProc {
	t.Helper()
	dir := filepath.Join(p.root, strconv.Itoa(pid))
	if err := os.Rename(p.dir, dir); err != nil {
		t.Fatal(err)
	}
	p.dir = dir
	return p
}

func noSignal(t *testing.T) func(uint32) error {
	return func(uint32) error {
		t.Error("signalled")
		return nil
	}
}

// TestLoadRefusesWrongPeer: the process behind the socket must be the
// target, by pid and euid (SO_PEERCRED) — a socket path that leads to
// another process's listener (here the test's, for a target of another
// pid or uid) gets no request.
func TestLoadRefusesWrongPeer(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T) (fakeProc, Request){
		"other pid": func(t *testing.T) (fakeProc, Request) {
			p := newFakeProc(t, jvmStatus, jvmMaps, "java\x00").movedTo(t, fakePID+1)
			r := request()
			r.PID = uint32(fakePID + 1)
			return p, r
		},
		"other uid": func(t *testing.T) (fakeProc, Request) {
			return newFakeProc(t, statusOf(fakePID, os.Geteuid()+1), jvmMaps, "java\x00"), request()
		},
	} {
		t.Run(name, func(t *testing.T) {
			p, r := setup(t)
			got := make(chan string, 1)
			p.listen(t, "0\nreturn code: 0\n", got)
			err := p.attacher(noSignal(t)).Load(context.Background(), r)
			if !errors.Is(err, ErrWrongPeer) {
				t.Fatalf("Load = %v, want ErrWrongPeer", err)
			}
			select {
			case req := <-got:
				if req != "" {
					t.Errorf("the wrong peer got a request: %q", req)
				}
			case <-time.After(time.Second):
				t.Error("the listener never saw the connection close")
			}
		})
	}
}

// TestLoadRefusesSymlinkedSocket: a symlink where the socket belongs is
// not connected through, even to a live listener.
func TestLoadRefusesSymlinkedSocket(t *testing.T) {
	p := newFakeProc(t, jvmStatus, jvmMaps, "java\x00")
	elsewhere := filepath.Join(p.dir, "root/tmp/other.sock")
	l, err := net.Listen("unix", elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	if err := os.Symlink(elsewhere, p.socket()); err != nil {
		t.Fatal(err)
	}
	err = p.attacher(noSignal(t)).Load(context.Background(), request())
	if err == nil || !strings.Contains(err.Error(), "not a socket") {
		t.Errorf("Load = %v, want a not-a-socket error", err)
	}
}

// TestLoadRefusesSymlinkedTmp: a /tmp in the JVM's root that is a
// symlink — resolved from /proc/<pid>/root it would point into the
// host's tree — is refused before any file is created or signal sent.
func TestLoadRefusesSymlinkedTmp(t *testing.T) {
	p := newFakeProc(t, jvmStatus, jvmMaps, "java\x00")
	host := t.TempDir()
	tmp := filepath.Join(p.dir, "root/tmp")
	if err := os.Remove(tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(host, tmp); err != nil {
		t.Fatal(err)
	}
	if err := p.attacher(noSignal(t)).Load(context.Background(), request()); err == nil {
		t.Fatal("Load followed a symlinked /tmp")
	}
	if entries, _ := os.ReadDir(host); len(entries) != 0 {
		t.Errorf("files created through the symlink: %v", entries)
	}
}

// TestLoadExistingTrigger: a trigger file already there is used when it
// is a regular file of the JVM's user (and left in place: it is not
// ours), and refuses the attach unsignalled when it is a symlink.
func TestLoadExistingTrigger(t *testing.T) {
	t.Run("own regular file", func(t *testing.T) {
		p := newFakeProc(t, jvmStatus, jvmMaps, "java\x00")
		trigger := filepath.Join(p.dir, "cwd/.attach_pid7")
		if err := os.WriteFile(trigger, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		got := make(chan string, 1)
		err := p.attacher(func(uint32) error {
			p.listen(t, "0\nreturn code: 0\n", got)
			return nil
		}).Load(context.Background(), request())
		if err != nil {
			t.Fatal(err)
		}
		<-got
		if _, err := os.Stat(trigger); err != nil {
			t.Errorf("another attacher's trigger was removed: %v", err)
		}
	})
	t.Run("planted symlink", func(t *testing.T) {
		p := newFakeProc(t, jvmStatus, jvmMaps, "java\x00")
		victim := filepath.Join(t.TempDir(), "victim")
		if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, filepath.Join(p.dir, "cwd/.attach_pid7")); err != nil {
			t.Fatal(err)
		}
		err := p.attacher(noSignal(t)).Load(context.Background(), request())
		if !errors.Is(err, ErrForeignTrigger) {
			t.Errorf("Load = %v, want ErrForeignTrigger", err)
		}
		if data, err := os.ReadFile(victim); err != nil || string(data) != "keep" {
			t.Errorf("the symlink's target changed: %q, %v", data, err)
		}
	})
}

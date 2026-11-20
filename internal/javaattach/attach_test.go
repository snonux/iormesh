package javaattach

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeNSPID is the fake JVM's pid inside its own pid namespace.
const fakeNSPID = 7

// fakePID is the fake JVM's pid as the tracer sees it: the test process
// itself, which plays the JVM's attach listener, so SO_PEERCRED names it.
var fakePID = os.Getpid()

// jvmStatus is a JVM's status file: the test's own pid and euid (what
// SO_PEERCRED reports of its listener), pid 7 inside its pid namespace,
// SIGQUIT (bit 2) caught.
var jvmStatus = statusOf(fakePID, os.Geteuid())

func statusOf(pid, euid int) string {
	return "Name:\tjava\nPid:\t" + strconv.Itoa(pid) + "\nNSpid:\t" + strconv.Itoa(pid) + "\t7\n" +
		"Uid:\t0\t" + strconv.Itoa(euid) + "\t0\t0\nGid:\t0\t0\t0\t0\n" +
		"SigIgn:\t0000000000000002\nSigCgt:\t2000000181005ccd\n"
}

const jvmMaps = "7f0000000000-7f0000001000 r-xp 00000000 fd:01 123 /usr/lib/jvm/java-25/lib/server/libjvm.so\n"

func TestParseStatus(t *testing.T) {
	info, err := parseStatus([]byte(jvmStatus))
	if err != nil {
		t.Fatal(err)
	}
	want := procInfo{euid: uint32(os.Geteuid()), nspid: fakeNSPID, quitCaught: true}
	if info != want {
		t.Errorf("parseStatus = %+v, want %+v", info, want)
	}
	// -Xrs: SIGQUIT ignored, not caught (the listener starts at startup).
	xrs := strings.Replace(jvmStatus, "SigCgt:\t2000000181005ccd", "SigCgt:\t2000000101001cc8", 1)
	if info, err := parseStatus([]byte(xrs)); err != nil || info.quitCaught {
		t.Errorf("-Xrs status: %+v, %v; want SIGQUIT not caught", info, err)
	}
	// No NSpid (kernels before 4.1): the Pid line.
	old := strings.Replace(jvmStatus, "NSpid:\t"+strconv.Itoa(fakePID)+"\t7\n", "", 1)
	if info, err := parseStatus([]byte(old)); err != nil || int(info.nspid) != fakePID {
		t.Errorf("status without NSpid: %+v, %v; want nspid %d", info, err, fakePID)
	}
	if _, err := parseStatus([]byte("Name:\tx\n")); err == nil {
		t.Error("a status without Uid and SigCgt parsed")
	}
}

// fakeProc is a /proc with one process under a short temporary root: a
// Unix socket path must stay below 108 bytes, which the test's own
// TMPDIR may not leave room for.
type fakeProc struct {
	root string
	dir  string // root/<pid>
}

func newFakeProc(t *testing.T, status, maps, cmdline string) fakeProc {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "ja")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	p := fakeProc{root: root, dir: filepath.Join(root, strconv.Itoa(fakePID))}
	for _, d := range []string{"cwd", "root/tmp"} {
		if err := os.MkdirAll(filepath.Join(p.dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{"status": status, "maps": maps, "cmdline": cmdline, "environ": ""}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(p.dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func (p fakeProc) socket() string {
	return filepath.Join(p.dir, "root/tmp", ".java_pid"+strconv.Itoa(fakeNSPID))
}

// listen plays the JVM's attach listener: it answers one request with
// reply and hands the request it read to got.
func (p fakeProc) listen(t *testing.T, reply string, got chan<- string) {
	t.Helper()
	l, err := net.Listen("unix", p.socket())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		buf := make([]byte, 4096)
		n, _ := c.Read(buf)
		got <- string(buf[:n])
		_, _ = io.WriteString(c, reply)
	}()
}

func (p fakeProc) attacher(signal func(uint32) error) Attacher {
	return Attacher{ProcRoot: p.root, Signal: signal}
}

func request() Request {
	return Request{PID: uint32(fakePID), Jar: "/opt/iormesh-agent.jar", Options: "spool=/s"}
}

// TestLoadStartsListener: no socket yet, so the trigger file is created
// in the JVM's cwd, one SIGQUIT is sent, and the load request goes to
// the socket the "JVM" then opens; the trigger file is gone afterwards.
func TestLoadStartsListener(t *testing.T) {
	p := newFakeProc(t, jvmStatus, jvmMaps, "java\x00-jar\x00app.jar\x00")
	got := make(chan string, 1)
	signals := 0
	trigger := filepath.Join(p.dir, "cwd", ".attach_pid"+strconv.Itoa(fakeNSPID))
	err := p.attacher(func(pid uint32) error {
		signals++
		if _, err := os.Stat(trigger); err != nil {
			t.Errorf("SIGQUIT without a trigger file: %v", err)
		}
		p.listen(t, "0\nreturn code: 0\n", got)
		return nil
	}).Load(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if signals != 1 {
		t.Errorf("%d SIGQUITs, want 1", signals)
	}
	if req := <-got; req != string(loadRequest("/opt/iormesh-agent.jar", "spool=/s")) {
		t.Errorf("request %q", req)
	}
	if _, err := os.Stat(trigger); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("trigger file left behind: %v", err)
	}
}

// TestLoadExistingSocket: an attach listener already running gets no
// SIGQUIT — it would answer with a thread dump on the app's stdout.
func TestLoadExistingSocket(t *testing.T) {
	p := newFakeProc(t, jvmStatus, jvmMaps, "java\x00")
	got := make(chan string, 1)
	p.listen(t, "0\nreturn code: 0\n", got)
	err := p.attacher(func(uint32) error {
		t.Error("SIGQUIT sent to a JVM whose listener runs")
		return nil
	}).Load(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	<-got
}

// TestLoadRefusesUnsafeTargets: a process that is no JVM, a JVM without
// a SIGQUIT handler, and one with the attach mechanism disabled are
// never signalled.
func TestLoadRefusesUnsafeTargets(t *testing.T) {
	noQuit := strings.Replace(jvmStatus, "SigCgt:\t2000000181005ccd", "SigCgt:\t0000000000000000", 1)
	for _, tc := range []struct {
		name, status, maps, cmdline string
		want                        error
	}{
		{"not a JVM", jvmStatus, "7f00-7f01 r-xp 0 fd:01 1 /usr/bin/python3\n", "python3\x00", ErrNotJVM},
		{"no SIGQUIT handler", noQuit, jvmMaps, "java\x00", ErrNoQuitHandler},
		{"attach disabled", jvmStatus, jvmMaps, "java\x00-XX:+DisableAttachMechanism\x00", ErrAttachDisabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newFakeProc(t, tc.status, tc.maps, tc.cmdline)
			err := p.attacher(func(uint32) error {
				t.Error("signalled an unsafe target")
				return nil
			}).Load(context.Background(), request())
			if !errors.Is(err, tc.want) {
				t.Errorf("Load = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestLoadDisabledByEnvironment: -XX:+DisableAttachMechanism in
// JAVA_TOOL_OPTIONS counts like on the command line.
func TestLoadDisabledByEnvironment(t *testing.T) {
	p := newFakeProc(t, jvmStatus, jvmMaps, "java\x00")
	env := "PATH=/bin\x00JAVA_TOOL_OPTIONS=-Xmx1g -XX:+DisableAttachMechanism\x00"
	if err := os.WriteFile(filepath.Join(p.dir, "environ"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	if !attachDisabled(p.root, uint32(fakePID)) {
		t.Error("JAVA_TOOL_OPTIONS' flag not seen")
	}
}

// TestLoadTimesOut: a JVM that never opens its socket ends the attach
// at the caller's deadline, with the trigger file removed.
func TestLoadTimesOut(t *testing.T) {
	p := newFakeProc(t, jvmStatus, jvmMaps, "java\x00")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := p.attacher(func(uint32) error { return nil }).Load(ctx, request())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Load = %v, want a deadline error", err)
	}
	if _, err := os.Stat(filepath.Join(p.dir, "cwd", ".attach_pid7")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("trigger file left behind: %v", err)
	}
}

// TestLoadSilentListenerTimesOut: a socket that accepts but never
// answers cannot hold the attach past its deadline.
func TestLoadSilentListenerTimesOut(t *testing.T) {
	p := newFakeProc(t, jvmStatus, jvmMaps, "java\x00")
	l, err := net.Listen("unix", p.socket())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := p.attacher(nil).Load(ctx, request()); err == nil {
		t.Fatal("Load succeeded against a silent listener")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Load took %v past a 100ms deadline", d)
	}
}

// TestLoadRefusesNonSocket: a regular file planted where the socket
// belongs is not connected to.
func TestLoadRefusesNonSocket(t *testing.T) {
	p := newFakeProc(t, jvmStatus, jvmMaps, "java\x00")
	if err := os.WriteFile(p.socket(), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	err := p.attacher(nil).Load(context.Background(), request())
	if err == nil || !strings.Contains(err.Error(), "not a socket") {
		t.Errorf("Load = %v, want a not-a-socket error", err)
	}
}

func TestLoadNeedsAbsoluteJar(t *testing.T) {
	r := request()
	r.Jar = "agent.jar"
	if err := New().Load(context.Background(), r); err == nil {
		t.Error("a relative jar path was accepted")
	}
}

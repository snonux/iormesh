// Command edgepeer drives the M2 edge cases the Java workload cannot force
// deterministically, for the integration suite (docs/milestones.md M2
// acceptance: "PID reuse" and "socket races"; docs/testing.md, "M2
// acceptance"):
//
//   - pid-target -dir D: the traced process whose PID gets reused. It
//     brackets itself as pid-reuse, writes D/pid-reuse-target.dat and
//     exits.
//   - pid-keeper: a second traced process that keeps the session alive
//     while the PID is reused. It brackets itself as pid-reuse-keeper
//     around a read of stdin to EOF.
//   - pid-reuse -pid N -dir D: an untraced launcher that lands a new
//     process exactly on the freed PID N with clone3's set_tid (no fork
//     loop, no race with the host's PID allocator) and waits for it.
//   - pid-squat -dir D: what the launcher's child execs: it brackets
//     itself as pid-reuse-squatter, writes D/pid-reuse-squatter.dat,
//     prints "squatting <pid>" and holds the PID until stdin reaches EOF.
//   - sock-races: one process running the socket races in a row (refused
//     and unreachable connects, a reset in the accept queue, a reset
//     mid-stream, a shutdown racing a blocked accept), bracketed as
//     sock-races. Each race prints one line of facts (fds and ports) the
//     test checks the trace against.
//   - port-owner -port P -dir D: listens on 127.0.0.1:P, then brackets
//     itself as port-follow around a write of D/port-owner-<pid>.dat,
//     prints "worked" and holds the port until stdin reaches EOF: one
//     incarnation of an app a -listen-port session follows across a
//     restart (task mh4).
//
// The gated roles (pid-target, pid-keeper, sock-races, port-owner) follow the Java
// workload's harness protocol (workload Main.java): the PID is the first
// stdout line, and nothing runs until $IORMESH_WORKLOAD_STARTUP_FILE
// exists, so the tracer is attached before the first bracketed syscall.
// Markers are failed opens of /iormesh-marker/<scenario>/<edge>, exactly
// what the Java workload's Marker does.
//
// Every scenario syscall is a raw one from package syscall on the main
// thread (pinned with LockOSThread, so TID == PID), never through Go's
// netpoller: the trace then holds exactly the syscalls below. Exit codes:
// 1 failure, 2 usage.
//
// Built cgo-free by `mage workloadBuild`, like workload/echopeer, and
// with its debug settings below (no cgroup reads by the runtime).
//
//go:debug containermaxprocs=0
//go:debug updatemaxprocs=0
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"time"
)

const (
	markerRoot = "/iormesh-marker/"
	// startupFileEnv is the harness's startup gate (workload Main.java).
	startupFileEnv = "IORMESH_WORKLOAD_STARTUP_FILE"
	startupTimeout = 30 * time.Second
	startupPoll    = 10 * time.Millisecond
)

// Pin the main goroutine to the main thread: the scenario's syscalls
// then all carry TID == PID.
func init() {
	runtime.LockOSThread()
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	role, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(role, flag.ExitOnError)
	dir := fs.String("dir", "", "directory for the role's files")
	pid := fs.Int("pid", 0, "the freed PID to land on (pid-reuse)")
	port := fs.Int("port", 0, "the TCP port to listen on (port-owner)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		usage()
	}
	run, ok := roles(*dir, *pid, *port)[role]
	if !ok || (needsDir(role) && *dir == "") || (role == "pid-reuse" && *pid <= 0) ||
		(role == "port-owner" && (*port <= 0 || *port > 65535)) {
		usage()
	}
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "edgepeer %s: %v\n", role, err)
		os.Exit(1)
	}
}

// roles maps each role to its body; the gated ones go through gated.
// port-owner binds before its gate (portOwner), so it calls gated itself.
func roles(dir string, pid, port int) map[string]func() error {
	return map[string]func() error{
		"port-owner": func() error { return portOwner(port, dir) },
		"pid-target": func() error { return gated(func() error { return pidTarget(dir) }) },
		"pid-keeper": func() error { return gated(pidKeeper) },
		"pid-reuse":  func() error { return pidReuse(pid, dir) },
		"pid-squat":  func() error { return pidSquat(dir) },
		"sock-races": func() error { return gated(sockRaces) },
	}
}

func needsDir(role string) bool {
	return role == "pid-target" || role == "pid-reuse" || role == "pid-squat" || role == "port-owner"
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: edgepeer pid-target -dir D | pid-keeper | "+
		"pid-reuse -pid N -dir D | pid-squat -dir D | sock-races | port-owner -port P -dir D")
	os.Exit(2)
}

// gated prints the PID, waits for the startup gate and runs body.
func gated(body func() error) error {
	if _, err := fmt.Println(os.Getpid()); err != nil {
		return err
	}
	if err := waitStartup(); err != nil {
		return err
	}
	return body()
}

// waitStartup polls for the startup file; an unset gate means no tracer
// to wait for (a standalone run), as in the Java workload.
func waitStartup() error {
	path := os.Getenv(startupFileEnv)
	if path == "" {
		return nil
	}
	deadline := time.Now().Add(startupTimeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %v waiting for %s", startupTimeout, path)
		}
		time.Sleep(startupPoll)
	}
}

// bracketed runs body between the scenario's begin and end markers. A
// failure returns before the end marker, so a failed run never has a
// complete bracket (as in the Java workload's Main.bracketed).
func bracketed(scenario string, body func() error) error {
	if err := marker(scenario, "begin"); err != nil {
		return err
	}
	if err := body(); err != nil {
		return err
	}
	return marker(scenario, "end")
}

// marker opens the reserved marker path, which must not exist: the
// tracer sees the failed openat and its path, nothing is created.
func marker(scenario, edge string) error {
	path := markerRoot + scenario + "/" + edge
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err == nil {
		_ = syscall.Close(fd) // already failing; the first error wins
		return fmt.Errorf("marker path exists: %s", path)
	}
	// stderr, so stdout stays the facts protocol.
	fmt.Fprintln(os.Stderr, "marker "+scenario+"/"+edge)
	return nil
}

// writeFile writes data to path with O_CREAT|O_TRUNC — one openat, one
// write, one close.
func writeFile(path string, data []byte) error {
	fd, err := syscall.Open(path,
		syscall.O_WRONLY|syscall.O_CREAT|syscall.O_TRUNC|syscall.O_CLOEXEC, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	_, werr := retry(func() (int, error) { return syscall.Write(fd, data) })
	if cerr := syscall.Close(fd); werr == nil {
		werr = cerr
	}
	return werr
}

// readToEOF reads fd until EOF: how the keeper and the squatter hold on
// until the test closes their stdin.
func readToEOF(fd int) error {
	buf := make([]byte, 64)
	for {
		n, err := retry(func() (int, error) { return syscall.Read(fd, buf) })
		if err != nil {
			return fmt.Errorf("read fd %d: %w", fd, err)
		}
		if n == 0 {
			return nil
		}
	}
}

// fact prints one line of the facts protocol: a race's name and its
// key=value pairs, in order.
func fact(name string, kv ...any) error {
	line := name
	for i := 0; i+1 < len(kv); i += 2 {
		line += fmt.Sprintf(" %v=%v", kv[i], kv[i+1])
	}
	_, err := fmt.Println(line)
	return err
}

// retry repeats a syscall interrupted before it moved data. Go installs
// its signal handlers with SA_RESTART, so EINTR should not happen; if it
// does, the extra attempt is a visible extra event, not a failure.
func retry(f func() (int, error)) (int, error) {
	for {
		n, err := f()
		if !errors.Is(err, syscall.EINTR) {
			return n, err
		}
	}
}

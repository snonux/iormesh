//go:build mage

// Magefile for iormesh: the Java workload targets (M0); their process
// harness is in magefile_workload.go. The tracer build, BPF and
// generation targets lifted from ior live beside it in magefile_bpf.go,
// magefile_generate.go and magefile_fmt.go (docs/implementation.md,
// "M1 — walking skeleton", step 1).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
)

const (
	// buildDir holds every build output - the binaries, the jars and the
	// gates' test binaries - so none lands at the repository root
	// (docs/decisions.md #30, tasks rk4/uk4). It is gitignored and
	// removed by `mage clean`; dist/ stays the release output.
	buildDir           = "bin"
	workloadSrcDir     = "workload/src"
	workloadClassesDir = "workload/classes"
	workloadJar        = buildDir + "/workload.jar"
	workloadMainClass  = "iormesh.workload.Main"
	// The non-java-peer scenario's Go peer, built next to the jar (both in
	// buildDir), where NonJavaPeer looks for it by default.
	echoPeerBinary = buildDir + "/echopeer"
	echoPeerPkg    = "./workload/echopeer"
	// The integration suite's Go helper for the PID reuse and socket
	// race edge cases (integrationtests/pidreuse_test.go, sockrace_test.go).
	edgePeerBinary = buildDir + "/edgepeer"
	edgePeerPkg    = "./workload/edgepeer"
	// The workload targets Java 21 (docs/decisions.md #5) whatever newer
	// JDK builds it, so it never depends on post-21 APIs.
	workloadRelease = "21"
)

// WorkloadBuild compiles the Java workload with the JDK alone (no Maven or
// Gradle, so the traced app has no build-time dependencies), bundles
// each scenario's expect.json so `--expect <name>` can print it, and
// builds the Go helpers: the non-java-peer scenario's peer and the
// integration suite's edgepeer.
func WorkloadBuild() error {
	if err := EchoPeerBuild(); err != nil {
		return err
	}
	if err := EdgePeerBuild(); err != nil {
		return err
	}
	srcs, err := javaSources(workloadSrcDir)
	if err != nil {
		return err
	}
	// A clean class dir keeps classes of deleted sources out of the jar.
	if err := os.RemoveAll(workloadClassesDir); err != nil {
		return err
	}
	args := append([]string{"--release", workloadRelease, "-Xlint:all", "-Werror",
		"-d", workloadClassesDir}, srcs...)
	if err := sh.RunV("javac", args...); err != nil {
		return err
	}
	// No ensureBuildDir: EchoPeerBuild ran first, and go build -o
	// bin/echopeer created bin/. The workload jars are not released, but
	// they are dated like the agent jar (jarCreate, task bm4) so that a
	// rebuild of the same commit gives the same bytes there too.
	date, err := sourceDate()
	if err != nil {
		return err
	}
	return jarCreate(date, "--file", workloadJar,
		"--main-class", workloadMainClass,
		"-C", workloadClassesDir, ".",
		"-C", "workload", "scenarios")
}

// EchoPeerBuild builds bin/echopeer, the non-JVM end of the non-java-peer
// scenario. CGO_ENABLED=0: it must not need the libbpfgo environment or
// libc to run, and a static binary execs the same everywhere.
func EchoPeerBuild() error {
	return sh.RunWithV(map[string]string{"CGO_ENABLED": "0"},
		"go", "build", "-o", echoPeerBinary, echoPeerPkg)
}

// EdgePeerBuild builds bin/edgepeer, the integration suite's helper for
// the edge cases a JVM cannot force deterministically (PID reuse, socket
// races). Cgo-free for the same reasons as EchoPeerBuild.
func EdgePeerBuild() error {
	return sh.RunWithV(map[string]string{"CGO_ENABLED": "0"},
		"go", "build", "-o", edgePeerBinary, edgePeerPkg)
}

// ensureBuildDir creates buildDir for the producers whose tool does not
// create an output's parent itself (jar --file, cp); go build -o does.
func ensureBuildDir() error {
	return os.MkdirAll(buildDir, 0o755)
}

// javaSources lists every .java file under root, sorted by WalkDir.
func javaSources(root string) ([]string, error) {
	var srcs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".java") {
			srcs = append(srcs, path)
		}
		return nil
	})
	if err == nil && len(srcs) == 0 {
		err = fmt.Errorf("no java sources under %s", root)
	}
	return srcs, err
}

// WorkloadTest runs every scenario without a tracer (--self-test), then
// checks the gates, the two-process runs (tcp-loopback, udp-loopback,
// unix-socket), every scenario's --expect output, syscall-burst's result
// line, the exit codes of bad invocations, that a failed run leaves a
// directory it did not create alone, the skip exit code, and
// restart's and non-java-peer's own failure modes (magefile_restartpeer.go)
// and M3's HTTP pairs, failure modes, OTEL flavor and third-party HTTP
// module (magefile_m3workload.go, magefile_libs.go), and M4's
// four-process microservices topology (magefile_m4workload.go).
func WorkloadTest() error {
	mg.Deps(WorkloadBuild, WorkloadOtelBuild, WorkloadLibsBuild)
	if err := sh.RunV("java", "-jar", workloadJar, "--self-test"); err != nil {
		return err
	}
	checks := []func() error{
		checkGatedRun,
		checkTCPLoopback,
		checkUDPLoopback,
		checkUnixSocket,
		checkExpectations,
		checkSyscallBurst,
		checkUsageErrors,
		checkRunFailures,
		checkForeignDirSurvives,
		checkSkip,
		checkRestartPeer,
		checkM3Scenarios,
		checkM4Scenarios,
	}
	for _, check := range checks {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

// checkGatedRun proves the harness gates hold: the PID comes first on
// stdout, no marker fires while the startup file is missing, and the
// process outlives its scenario until the hold file appears.
func checkGatedRun() error {
	dir, err := os.MkdirTemp("", "iormesh-gate-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	startup, hold := filepath.Join(dir, "startup"), filepath.Join(dir, "hold")

	run, err := startGated(startup, hold)
	if err != nil {
		return err
	}
	defer run.kill()
	if err := run.expectPID(); err != nil {
		return err
	}
	run.drainStdout()
	if err := run.expectQuiet("before the startup file", time.Second); err != nil {
		return err
	}
	if err := touch(startup); err != nil {
		return err
	}
	if err := run.expectStderr("marker noop/end"); err != nil {
		return err
	}
	if err := run.expectQuiet("before the hold file", 500*time.Millisecond); err != nil {
		return err
	}
	if err := touch(hold); err != nil {
		return err
	}
	return run.wait()
}

// checkTCPLoopback runs tcp-loopback as the harness will in M1: a server
// on a kernel-picked port (the default --port 0), then a client started
// only after the server's `listening <port>` line, so the connect never
// races the listen. Both must finish their bracket and exit 0.
func checkTCPLoopback() error {
	return checkTwoRoles("tcp-loopback", nil, portClient)
}

// checkUDPLoopback runs udp-loopback's two roles the same way.
func checkUDPLoopback() error {
	return checkTwoRoles("udp-loopback", nil, portClient)
}

// checkUnixSocket runs unix-socket's two roles: the server binds its
// socket in a fresh directory and names it in its `listening <path>`
// line, which the client gets as --path. os.MkdirTemp's short /tmp path
// keeps the socket path under the kernel's 108-byte sun_path limit.
func checkUnixSocket() error {
	dir, err := os.MkdirTemp("", "iormesh-unix-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	return checkTwoRoles("unix-socket", []string{"--dir", dir},
		func(addr string) []string { return []string{"--path", addr} })
}

// portClient turns a server's `listening <port>` address into the
// client's options, rejecting anything but a valid port.
func portClient(addr string) []string {
	if port, err := strconv.Atoi(addr); err != nil || port < 1 || port > 65535 {
		return nil
	}
	return []string{"--port", addr}
}

// checkTwoRoles starts scenario's server with serverOpts, reads its
// `listening <addr>` line, starts the client with clientOpts(addr) and
// expects both to finish their bracket and exit 0. A nil clientOpts
// result means the server announced a malformed address.
func checkTwoRoles(scenario string, serverOpts []string, clientOpts func(string) []string) error {
	return checkTwoRolesJar(workloadJar, scenario, serverOpts, clientOpts)
}

// checkTwoRolesJar is checkTwoRoles with both roles run from jar.
func checkTwoRolesJar(jar, scenario string, serverOpts []string,
	clientOpts func(string) []string) error {
	server, err := startJar(jar, nil, append([]string{scenario, "--role", "server"}, serverOpts...)...)
	if err != nil {
		return err
	}
	defer server.kill()
	if err := server.expectPID(); err != nil {
		return err
	}
	addr, err := server.expectListening()
	if err != nil {
		return err
	}
	server.drainStdout()
	opts := clientOpts(addr)
	if opts == nil {
		return fmt.Errorf("%s: bad listening address %q", server.name(), addr)
	}
	client, err := startJar(jar, nil, append([]string{scenario, "--role", "client"}, opts...)...)
	if err != nil {
		return err
	}
	defer client.kill()
	if err := client.expectPID(); err != nil {
		return err
	}
	client.drainStdout()
	// Client first: a server stuck in accept would otherwise hide the
	// client failure that caused it behind a timeout.
	if err := finish(client, scenario); err != nil {
		return err
	}
	return finish(server, scenario)
}

// finish expects the scenario's end marker on stderr, then exit code 0.
func finish(r *gatedRun, scenario string) error {
	if err := r.expectStderr("marker " + scenario + "/end"); err != nil {
		return err
	}
	return r.wait()
}

// checkSyscallBurst runs the load scenario standalone with more threads
// than ops divide evenly into, and asserts its result line: the PID
// first, then exactly the ops, threads and syscall count asked for (two
// syscalls per op) and a positive elapsed time — the line
// scripts/perf-baseline.sh parses, so a format change fails here first.
func checkSyscallBurst() error {
	run, err := startRun(nil, "syscall-burst", "--ops", "1001", "--threads", "3")
	if err != nil {
		return err
	}
	defer run.kill()
	if err := run.expectPID(); err != nil {
		return err
	}
	line, err := run.nextStdout()
	if err != nil {
		return err
	}
	run.drainStdout()
	var elapsed int64
	_, err = fmt.Sscanf(line, "syscall-burst ops=1001 threads=3 syscalls=2002 elapsed_ns=%d", &elapsed)
	if err != nil || elapsed <= 0 {
		return fmt.Errorf("%s: bad result line %q", run.name(), line)
	}
	return finish(run, "syscall-burst")
}

// checkExpectations asserts --expect prints the bundled expect.json as
// valid JSON naming its scenario, for every scenario --list names, so a
// new scenario without an expect.json fails here rather than in M1.
func checkExpectations() error {
	out, err := runBounded(nil, "--list")
	if err != nil {
		return fmt.Errorf("--list: %w", err)
	}
	names := strings.Fields(string(out))
	if len(names) == 0 {
		return errors.New("--list: no scenarios")
	}
	for _, name := range names {
		if err := checkExpectation(name); err != nil {
			return err
		}
	}
	return nil
}

// checkExpectation asserts `--expect name` prints valid JSON whose
// scenario field is name.
func checkExpectation(name string) error {
	out, err := runBounded(nil, "--expect", name)
	if err != nil {
		return fmt.Errorf("--expect %s: %w", name, err)
	}
	var exp struct {
		Scenario string `json:"scenario"`
	}
	if err := json.Unmarshal(out, &exp); err != nil {
		return fmt.Errorf("--expect %s: invalid JSON: %w", name, err)
	}
	if exp.Scenario != name {
		return fmt.Errorf("--expect %s: scenario %q", name, exp.Scenario)
	}
	return nil
}

// checkUsageErrors asserts each malformed invocation exits with the usage
// code 2, before any scenario I/O.
func checkUsageErrors() error {
	bad := [][]string{
		{},
		{"no-such-scenario"},
		{"noop", "--role", "bogus"},
		{"noop", "--role"},
		{"noop", "--role", "solo", "--role", "client"},
		{"--self-test", "extra"},
		{"--list", "extra"},
		{"noop", "stray"},
		{"--expect"},
		{"--expect", "no-such-scenario"},
		// Scenario-level option checks (Options.java).
		{"tcp-loopback", "--role", "client"},
		{"tcp-loopback", "--role", "client", "--port", "abc"},
		{"tcp-loopback", "--role", "client", "--port", "70000"},
		{"tcp-loopback", "--role", "client", "--port", "0"},
		{"tcp-loopback", "--role", "solo"},
		{"file-basic", "--role", "server"},
		{"file-basic", "--bogus", "x"},
		{"syscall-burst", "--ops", "abc"},
		{"syscall-burst", "--ops", "0"},
		{"syscall-burst", "--ops", "-5"},
		{"syscall-burst", "--ops", "99999999999"},
		{"syscall-burst", "--threads", "x"},
		{"syscall-burst", "--threads", "0"},
		{"syscall-burst", "--threads", "1025"},
		{"syscall-burst", "--ops", "3", "--threads", "4"},
		{"syscall-burst", "--role", "client"},
		{"syscall-burst", "--bogus", "x"},
	}
	// The M2 file and fd scenarios take --dir only and run solo.
	for _, name := range m2FileScenarios {
		bad = append(bad, []string{name, "--role", "client"}, []string{name, "--bogus", "x"})
	}
	// The M2 IPC, signal and address-family scenarios run solo with no
	// options at all, so even --dir is a usage error.
	for _, name := range m2SoloScenarios {
		bad = append(bad, []string{name, "--role", "server"}, []string{name, "--dir", "x"})
	}
	bad = append(bad, m2RoleUsageErrors...)
	bad = append(bad, m2AppUsageErrors...)
	bad = append(bad, m3UsageErrors...)
	for _, args := range bad {
		if err := expectExit(2, args...); err != nil {
			return err
		}
	}
	return nil
}

// m2FileScenarios are M2's file-surface and fd-lifecycle scenarios
// (task ma4): solo, --dir their only option, so they share one set of
// usage and run-failure checks.
var m2FileScenarios = []string{"fd-lifecycle", "file-attrs", "file-lifecycle", "file-links"}

// m2SoloScenarios are M2's option-less solo scenarios (task na4).
var m2SoloScenarios = []string{"pipe-ipc", "signals", "tcp-ipv4", "tcp-ipv6"}

// m2RoleUsageErrors are malformed invocations of M2's two-role
// scenarios (task na4): each role accepts only its own options.
var m2RoleUsageErrors = [][]string{
	{"unix-socket", "--role", "solo"},
	{"unix-socket", "--role", "client"},
	{"unix-socket", "--role", "client", "--path", ""},
	{"unix-socket", "--role", "client", "--path", "s", "--dir", "x"},
	{"unix-socket", "--role", "server", "--path", "s"},
	{"udp-loopback", "--role", "solo"},
	{"udp-loopback", "--role", "client"},
	{"udp-loopback", "--role", "client", "--port", "abc"},
	{"udp-loopback", "--role", "client", "--port", "0"},
	{"udp-loopback", "--role", "server", "--port", "70000"},
	{"udp-loopback", "--role", "server", "--dir", "x"},
}

// checkRunFailures asserts a well-formed run that fails at run time exits
// with code 1, not the usage code: a client with nobody listening, and a
// --dir that does not exist (every scenario that takes --dir).
func checkRunFailures() error {
	port, err := closedPort()
	if err != nil {
		return err
	}
	failing := [][]string{
		{"tcp-loopback", "--role", "client", "--port", fmt.Sprint(port)},
		{"file-basic", "--dir", "/nonexistent/iormesh-workload"},
		{"syscall-burst", "--ops", "10", "--dir", "/nonexistent/iormesh-workload"},
	}
	for _, name := range m2FileScenarios {
		failing = append(failing, []string{name, "--dir", "/nonexistent/iormesh-workload"})
	}
	// unix-socket fails like tcp-loopback: no directory for the server's
	// socket, no socket for the client. udp-loopback has no such check:
	// an unconnected UDP client never learns its datagram went nowhere.
	failing = append(failing,
		[]string{"unix-socket", "--role", "server", "--dir", "/nonexistent/iormesh-workload"},
		[]string{"unix-socket", "--role", "client", "--path", "/nonexistent/iormesh.sock"})
	failing = append(failing, m2AppRunFailures...)
	for _, args := range failing {
		if err := expectExit(1, args...); err != nil {
			return err
		}
	}
	return nil
}

// treeScenarios build their own <name>.d directory inside --dir.
var treeScenarios = []string{"fd-lifecycle", "file-lifecycle", "file-links"}

// checkForeignDirSurvives: a <name>.d the run did not create (left in a
// reused --dir) makes the scenario's mkdir fail, so the run fails (exit
// 1) — and its failure cleanup must leave that directory alone.
func checkForeignDirSurvives() error {
	for _, name := range treeScenarios {
		if err := foreignDirSurvives(name); err != nil {
			return err
		}
	}
	return nil
}

func foreignDirSurvives(name string) error {
	return foreignDirSurvivesRun(name, name)
}

// foreignDirSurvivesRun is foreignDirSurvives for a run whose tree is
// sub.d rather than <scenario>.d — a microservices service makes
// <role>.d — with args (the scenario and its options) plus --dir.
func foreignDirSurvivesRun(sub string, args ...string) error {
	dir, err := os.MkdirTemp("", "iormesh-foreign-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	keep := filepath.Join(dir, sub+".d", "keep.dat")
	if err := os.Mkdir(filepath.Dir(keep), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(keep, []byte("not yours"), 0o644); err != nil {
		return err
	}
	if err := expectExit(1, append(args, "--dir", dir)...); err != nil {
		return err
	}
	if _, err := os.Stat(keep); err != nil {
		return fmt.Errorf("%q deleted a directory it did not create: %w", args, err)
	}
	return nil
}

// checkSkip asserts a scenario the host cannot run exits with the skip
// code 3, not as a failure: tcp-ipv6 on a JVM told to use IPv4 only
// (JDK_JAVA_OPTIONS reaches the launcher without a jar-side flag).
func checkSkip() error {
	env := []string{"JDK_JAVA_OPTIONS=-Djava.net.preferIPv4Stack=true"}
	return expectExitEnv(env, 3, "tcp-ipv6")
}

// closedPort returns a loopback port nothing listens on: one the kernel
// just handed out and that was closed again. The kernel picks such ports
// from the whole ephemeral range, so another process taking this one in
// the second before the client connects is unlikely enough for a check.
func closedPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	return port, l.Close()
}

// expectExit runs the workload jar and fails unless it exits with code.
func expectExit(code int, args ...string) error {
	return expectExitEnv(nil, code, args...)
}

// expectExitEnv is expectExit with env added to the jar's environment.
func expectExitEnv(env []string, code int, args ...string) error {
	return expectExitJar(workloadJar, env, code, args...)
}

// expectExitJar is expectExitEnv for any workload jar.
func expectExitJar(jar string, env []string, code int, args ...string) error {
	_, err := runBoundedJar(jar, env, args...)
	if err == nil && code == 0 {
		return nil
	}
	return wantExitCode(err, code, fmt.Sprintf("workload %q", args))
}

// runBounded runs the workload jar to completion, with env added to its
// environment, and returns its stdout. gateWait bounds it, so a run that
// blocks (say, a client whose "closed" port got reused by a silent
// listener) fails the check instead of hanging it.
func runBounded(env []string, args ...string) ([]byte, error) {
	return runBoundedJar(workloadJar, env, args...)
}

// runBoundedJar is runBounded for any workload jar.
func runBoundedJar(jar string, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gateWait)
	defer cancel()
	cmd := jarCmd(ctx, jar, args...)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	return cmd.Output()
}

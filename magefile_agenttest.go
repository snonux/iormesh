//go:build mage

// The Java agent's tests (M3 steps 1–2): unit tests as a plain Java main,
// then smoke runs with -javaagent that show the hooks fire and the JVM
// still runs. The build is in magefile_agent.go.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
)

// agentTestDir holds the compiled tests and the smoke target.
var agentTestDir = filepath.Join(agentBuildDir, "test")

// smokeKinds are the hook kinds AgentSmoke's I/O must close at least one
// bracket of: every kind the hook tables use (HookTable.KINDS), and http,
// the HTTP hooks' requests recorded (task 3b4).
var smokeKinds = []string{"open", "read", "write", "seek", "close", "socket",
	"bind", "connect", "listen", "accept", "shutdown", "unlink", "rename", "stat", "http"}

// AgentTest builds the agent and runs its unit tests and smoke runs with
// the java on PATH, then again with each JDK in IORMESH_AGENT_JDKS (a
// colon-separated list of JAVA_HOMEs, e.g. a JDK 17 to check the jdk17
// hook table), and finally runs the workload's file-basic scenario under
// the agent. Last, internal/agentspool's Java-driven tests read rings
// the real agent wrote (decision #27): a concurrent RingStress writer and
// the workload under the agent with a spool directory.
func AgentTest() error {
	mg.SerialDeps(AgentBuild, WorkloadBuild)
	cp := strings.Join([]string{agentCoreDir, agentASMDir, agentBridgeDir}, ":")
	if err := javacAgent("agent/test/src", agentTestDir, cp); err != nil {
		return err
	}
	for _, java := range agentTestJavas() {
		if err := sh.RunV(java, "-cp", agentTestDir+":"+cp, "iormesh.agent.AgentTests"); err != nil {
			return fmt.Errorf("%s: unit tests: %w", java, err)
		}
		if err := smokeAgent(java); err != nil {
			return err
		}
	}
	if err := smokeWorkloadFileBasic(); err != nil {
		return err
	}
	return agentSpoolJavaTests()
}

// agentSpoolJavaTests runs internal/agentspool's TestJava* tests, which
// skip unless these variables name the classes and jars (absolute, since
// go test runs in the package directory).
func agentSpoolJavaTests() error {
	abs := func(p string) string {
		a, err := filepath.Abs(p)
		if err != nil {
			return p
		}
		return a
	}
	env := map[string]string{
		"CGO_ENABLED":           "0",
		"IORMESH_AGENT_TEST_CP": abs(agentTestDir) + ":" + abs(agentBridgeDir),
		"IORMESH_AGENT_JAR":     abs(agentJar),
		"IORMESH_WORKLOAD_JAR":  abs(workloadJar),
	}
	return sh.RunWithV(env, "go", "test", "-count=1", "-v", "-run", "^TestJava",
		"./internal/agentspool/")
}

// agentTestJavas is the java on PATH plus bin/java of each JDK listed
// in IORMESH_AGENT_JDKS.
func agentTestJavas() []string {
	javas := []string{"java"}
	for _, home := range strings.Split(os.Getenv("IORMESH_AGENT_JDKS"), ":") {
		if home != "" {
			javas = append(javas, filepath.Join(home, "bin", "java"))
		}
	}
	return javas
}

// smokeAgent runs AgentSmoke under the agent, with boot-class bytecode
// verification on: it must print "ok", every
// table row of a loaded class must have been applied, and each kind in
// smokeKinds must have closed a bracket. A spool directory beside the
// dump gives the agent a ring (nobody reads it): the HTTP hooks record
// only into one, and "http" is among smokeKinds.
func smokeAgent(java string) error {
	debug, cleanup, err := agentDebugPath("smoke")
	if err != nil {
		return err
	}
	defer cleanup()
	spool := filepath.Join(filepath.Dir(debug), "spool")
	if err := os.Mkdir(spool, 0o700); err != nil {
		return err
	}
	// Boot classes are not verified by default; turning it on makes a
	// bad rewrite (e.g. a wrong stack map frame) fail here, not in the
	// field.
	out, err := exec.Command(java, "-XX:+UnlockDiagnosticVMOptions",
		"-XX:+BytecodeVerificationLocal", "-javaagent:"+agentJar+"=spool="+spool+",debug="+debug,
		"-cp", agentTestDir, "iormesh.agent.AgentSmoke").Output()
	if err != nil {
		return fmt.Errorf("%s: AgentSmoke under the agent: %w", java, err)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		return fmt.Errorf("%s: AgentSmoke printed %q, want ok", java, out)
	}
	return checkAgentDebug(java+" AgentSmoke", debug, smokeKinds)
}

// smokeWorkloadFileBasic runs the file-basic scenario under the agent:
// the workload's output contract must be intact — stdout is the PID
// alone, the markers are on stderr (next to the JVM's CDS note about
// the appended bootstrap path) — and its opens, write and read must
// have closed brackets.
func smokeWorkloadFileBasic() error {
	debug, cleanup, err := agentDebugPath("file-basic")
	if err != nil {
		return err
	}
	defer cleanup()
	cmd := exec.Command("java", "-javaagent:"+agentJar+"=debug="+debug,
		"-jar", workloadJar, "file-basic")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("file-basic under the agent: %w: %s", err, stderr.String())
	}
	if got := strings.TrimSpace(string(out)); got != strconv.Itoa(cmd.Process.Pid) {
		return fmt.Errorf("file-basic under the agent: stdout %q, want the PID %d",
			got, cmd.Process.Pid)
	}
	for _, m := range []string{"marker file-basic/begin", "marker file-basic/end"} {
		if !strings.Contains(stderr.String(), m+"\n") {
			return fmt.Errorf("file-basic under the agent: no %q on stderr: %s", m, stderr.String())
		}
	}
	return checkAgentDebug("file-basic", debug, []string{"open", "write", "read"})
}

// agentDebugPath returns a fresh path for the agent's debug dump and a
// cleanup that removes its temporary directory.
func agentDebugPath(name string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "iormesh-agent-test-")
	if err != nil {
		return "", nil, err
	}
	return filepath.Join(dir, name+".hooks.tsv"), func() { _ = os.RemoveAll(dir) }, nil
}

// checkAgentDebug reads the agent's debug dump (one row per hook: id,
// kind, class, method, descriptor, status, closed brackets) and fails on
// a "missing" row — a listed method this JDK lacks — or on a kind in
// kinds that closed no bracket.
func checkAgentDebug(run, path string, kinds []string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%s: no agent debug dump: %w", run, err)
	}
	closed := map[string]int64{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 7 {
			return fmt.Errorf("%s: bad debug row %q", run, line)
		}
		if f[5] == "missing" {
			return fmt.Errorf("%s: hook %s %s.%s%s missing from this JDK", run, f[0], f[2], f[3], f[4])
		}
		n, err := strconv.ParseInt(f[6], 10, 64)
		if err != nil {
			return fmt.Errorf("%s: bad count in %q: %w", run, line, err)
		}
		closed[f[1]] += n
	}
	for _, k := range kinds {
		if closed[k] == 0 {
			return fmt.Errorf("%s: no %s bracket closed (%v)", run, k, closed)
		}
	}
	fmt.Printf("%s: hooks fired %v\n", run, closed)
	return nil
}

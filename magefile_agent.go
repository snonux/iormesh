//go:build mage

// The Java agent's build (M3, docs/implementation.md "M3 — Java agent",
// steps 1–2): `mage agentBuild` → iormesh-agent.jar. Like the workload
// it is built with the JDK alone (no Maven or Gradle); unlike the
// workload it has one dependency, ASM, vendored under agent/third_party
// and shaded into the jar. Its tests are in magefile_agenttest.go.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/magefile/mage/sh"
)

const (
	agentJar      = "iormesh-agent.jar"
	agentBuildDir = "agent/build"
	// The agent runs on 17+ (PLAN.md decision #29), so it is compiled
	// for 17 whatever newer JDK builds it.
	agentRelease = "17"
	agentMain    = "iormesh.agent.Agent"
	// ASM, vendored with its license and provenance
	// (agent/third_party/asm/PROVENANCE); the hash pins the exact jar,
	// so a swapped or corrupted copy fails the build instead of being
	// shaded into the agent.
	asmJar    = "agent/third_party/asm/asm-9.11.1.jar"
	asmSHA256 = "ae4e6e10d59d0a1500317764b21881ad883f8550e33c66e6142f7032c6fb566c"
	// Where ASM's classes move in the agent jar, so the agent never
	// clashes with an ASM the traced application ships.
	asmFrom = "org/objectweb/asm/"
	asmTo   = "iormesh/agent/shaded/asm/"
	// The bridge jar's name inside the agent jar (agent/core Bridge.NESTED).
	bridgeNested = "iormesh-bridge.jar"
)

// Build outputs under agentBuildDir, one directory per step.
var (
	agentASMDir    = filepath.Join(agentBuildDir, "asm")
	agentBridgeDir = filepath.Join(agentBuildDir, "bridge")
	agentCoreDir   = filepath.Join(agentBuildDir, "core")
	agentBridgeJar = filepath.Join(agentBuildDir, bridgeNested)
)

// AgentBuild builds iormesh-agent.jar: the agent core with ASM shaded
// under iormesh.agent.shaded.asm, the hook tables, and the bridge jar
// (the bootstrap-visible Hooks and StackIds) nested as a resource that
// the agent appends to the bootstrap search path at install.
func AgentBuild() error {
	if err := verifyASM(); err != nil {
		return err
	}
	// A clean build dir keeps classes of deleted sources out of the jars.
	if err := os.RemoveAll(agentBuildDir); err != nil {
		return err
	}
	for _, step := range []func() error{relocateASM, buildBridge, buildAgentCore} {
		if err := step(); err != nil {
			return err
		}
	}
	return packAgent()
}

// verifyASM checks the vendored ASM jar against its pinned SHA-256.
func verifyASM() error {
	b, err := os.ReadFile(asmJar)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != asmSHA256 {
		return fmt.Errorf("%s: sha256 %s, want %s (see agent/third_party/asm/PROVENANCE)",
			asmJar, got, asmSHA256)
	}
	return nil
}

// relocateASM writes ASM's classes, moved to asmTo, into agentASMDir
// (agent/tools/Relocate.java, run by the JDK's source launcher).
func relocateASM() error {
	return sh.RunV("java", "agent/tools/Relocate.java", asmJar, agentASMDir, asmFrom, asmTo)
}

// buildBridge compiles the bridge and jars it on its own: it must reach
// the bootstrap loader as a separate jar, never as agent-jar classes.
func buildBridge() error {
	if err := javacAgent("agent/bridge/src", agentBridgeDir, ""); err != nil {
		return err
	}
	return sh.RunV("jar", "--create", "--file", agentBridgeJar, "-C", agentBridgeDir, ".")
}

// buildAgentCore compiles the core against the relocated ASM and copies
// in its resources: the hook tables and the nested bridge jar.
func buildAgentCore() error {
	if err := javacAgent("agent/core/src", agentCoreDir, agentASMDir); err != nil {
		return err
	}
	resDir := filepath.Join(agentCoreDir, "iormesh", "agent")
	tables, err := filepath.Glob("agent/core/resources/iormesh/agent/*.tsv")
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		return fmt.Errorf("no hook tables under agent/core/resources")
	}
	for _, src := range append(tables, agentBridgeJar) {
		if err := sh.Copy(filepath.Join(resDir, filepath.Base(src)), src); err != nil {
			return err
		}
	}
	return nil
}

// packAgent writes the agent jar: manifest, core classes and resources,
// and the relocated ASM classes.
func packAgent() error {
	manifest := filepath.Join(agentBuildDir, "MANIFEST.MF")
	err := os.WriteFile(manifest, []byte("Premain-Class: "+agentMain+"\n"+
		"Agent-Class: "+agentMain+"\n"+
		"Can-Retransform-Classes: true\n"), 0o644)
	if err != nil {
		return err
	}
	return sh.RunV("jar", "--create", "--file", agentJar, "--manifest", manifest,
		"-C", agentCoreDir, ".", "-C", agentASMDir, ".")
}

// javacAgent compiles every .java under src into out for agentRelease,
// warnings as errors; classpath may be empty.
func javacAgent(src, out, classpath string) error {
	srcs, err := javaSources(src)
	if err != nil {
		return err
	}
	args := []string{"--release", agentRelease, "-Xlint:all", "-Werror", "-d", out}
	if classpath != "" {
		args = append(args, "-cp", classpath)
	}
	return sh.RunV("javac", append(args, srcs...)...)
}

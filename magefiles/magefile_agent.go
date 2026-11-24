//go:build mage

// The Java agent's build (M3, docs/implementation.md "M3 — Java agent",
// steps 1–2): `mage agentBuild` → bin/iormesh-agent.jar. Like the workload
// it is built with the JDK alone (no Maven or Gradle); unlike the
// workload it has one dependency, ASM, vendored under agent/third_party
// and shaded into the jar. Its tests are in magefile_agenttest.go.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/magefile/mage/sh"
	"github.com/magefile/mage/target"

	"iormesh/internal/version"
)

const (
	// agentJar is the agent build's output in buildDir (decision #30);
	// its dist/ copy keeps the base name, the one mage sign signs.
	agentJar = buildDir + "/iormesh-agent.jar"
	// distDir holds the release artifacts mage sign signs.
	distDir       = "dist"
	agentBuildDir = "agent/build"
	// The agent runs on 17+ (PLAN.md decision #29), so it is compiled
	// for 17 whatever newer JDK builds it.
	agentRelease = "17"
	agentMain    = "iormesh.agent.Agent"
	// ASM, vendored with its license and provenance
	// (agent/third_party/asm/PROVENANCE); the hash pins the exact jar,
	// so a swapped or corrupted copy fails the build instead of being
	// shaded into the agent.
	asmJar     = "agent/third_party/asm/asm-9.11.1.jar"
	asmLicense = "agent/third_party/asm/LICENSE.txt"
	asmSHA256  = "ae4e6e10d59d0a1500317764b21881ad883f8550e33c66e6142f7032c6fb566c"
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

// AgentBuild builds bin/iormesh-agent.jar: the agent core with ASM shaded
// under iormesh.agent.shaded.asm, the hook tables, and the bridge jar
// (the bootstrap-visible Hooks and StackIds) nested as a resource that
// the agent appends to the bootstrap search path at install. A copy goes
// to dist/ as a release artifact (task kf4): `mage sign` signs it with
// the tracers, and `iormeshctl deploy` ships it to hosts whose projects
// use the agent. A copy over an older one leaves that one's signature
// behind, which no longer verifies, so a jar is never shipped unsigned
// by `mage sign`. A jar newer than all its inputs is kept (agentJarFresh),
// so the targets depending on it (Test and TestRace, task yk4) do not
// rebuild it on every run; its dist/ copy is refreshed only when missing
// or older.
func AgentBuild() error {
	if err := verifyASM(); err != nil {
		return err
	}
	fresh, err := agentJarFresh()
	if err != nil {
		return err
	}
	if !fresh {
		if err := buildAgentJar(); err != nil {
			return err
		}
	}
	return copyAgentToDist()
}

// agentJarInputs are what agentJar is built from: the agent's sources,
// resources and tools (not agent/build, the intermediates, nor agent/test),
// the vendored ASM, internal/version, whose release version packAgent
// stamps into the manifest. agentRecipeGlob adds the magefiles.
var agentJarInputs = []string{"agent/bridge", "agent/core", "agent/tools",
	"agent/third_party", "internal/version"}

// agentRecipeGlob is the build recipe itself (manifest text, --release and
// -Xlint flags, the ASM shading, the resources copied, javaSources): an
// edited recipe must not leave a stale jar for mage release to sign and
// ship, so editing any magefile rebuilds the jar, a cheap price for never
// missing one. It is a glob of the files, not target.Dir of the directory:
// mage writes and removes mage_output_file.go there on every run, so the
// directory's own mtime is always newer than the jar. The JDK is no input:
// an upgrade does not rebuild, which is fine while javacAgent compiles with
// --release agentRelease.
const agentRecipeGlob = "magefiles/*.go"

// agentBuildOutputs are the intermediates AgentTest compiles its tests
// against; a missing one makes the jar stale, so AgentBuild recreates it.
var agentBuildOutputs = []string{agentCoreDir, agentASMDir, agentBridgeDir}

// agentJarFresh reports whether agentJar exists, is newer than every file
// under agentJarInputs and agentRecipeGlob, and the intermediates in
// agentBuildOutputs exist.
func agentJarFresh() (bool, error) {
	for _, dir := range agentBuildOutputs {
		if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
			return false, nil
		} else if err != nil {
			return false, err
		}
	}
	stale, err := target.Dir(agentJar, agentJarInputs...)
	if err != nil || stale {
		return false, err
	}
	stale, err = target.Glob(agentJar, agentRecipeGlob)
	return !stale, err
}

// buildAgentJar builds agentJar from scratch, intermediates included.
func buildAgentJar() error {
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

// copyAgentToDist copies agentJar to distDir unless the copy there is
// already as new, which a fresh build never leaves it.
func copyAgentToDist() error {
	dst := filepath.Join(distDir, filepath.Base(agentJar))
	stale, err := target.Path(dst, agentJar)
	if err != nil || !stale {
		return err
	}
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		return err
	}
	return sh.Copy(dst, agentJar)
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
// in its resources: the hook tables, the nested bridge jar and ASM's
// license notice.
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
	// BSD-3 asks binary redistributions to carry ASM's notice.
	if err := os.MkdirAll(filepath.Join(agentCoreDir, "META-INF"), 0o755); err != nil {
		return err
	}
	return sh.Copy(filepath.Join(agentCoreDir, "META-INF", "LICENSE-asm.txt"), asmLicense)
}

// packAgent writes the agent jar: manifest, core classes and resources,
// and the relocated ASM classes. The manifest's Implementation-Version
// is the release version (internal/version), which `mage sign` requires
// to equal the version it signs the jar as (release.CheckStamp, task
// uf4): a jar left over from an older build is refused, not signed as
// the new release.
func packAgent() error {
	manifest := filepath.Join(agentBuildDir, "MANIFEST.MF")
	err := os.WriteFile(manifest, []byte("Premain-Class: "+agentMain+"\n"+
		"Agent-Class: "+agentMain+"\n"+
		"Can-Retransform-Classes: true\n"+
		"Implementation-Title: iormesh-agent\n"+
		"Implementation-Version: "+version.Version+"\n"), 0o644)
	if err != nil {
		return err
	}
	if err := ensureBuildDir(); err != nil {
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

package ctl

import (
	"fmt"
	"path/filepath"
	"strings"

	"iormesh/internal/federate"
	"iormesh/internal/release"
)

// The agent jar's half of deploy (task kf4, PLAN.md decision #28): a host
// with a project that uses the Java agent gets dist/iormesh-agent.jar
// shipped the way its tracer is — verified here against the pinned key
// before anything is pushed, staged next to the tracer, and installed by
// `sudo iormesh upgrade`, which verifies it again against the server's
// root-owned key and puts it at /opt/iormesh/lib/iormesh-agent.jar. A
// deployed session's -agent may name only that file (checkDeployFlags
// here, the confinement in the tracer), and a JVM started with
// -javaagent:/opt/iormesh/lib/iormesh-agent.jar=spool=… loads the same
// verified jar. A host without such a project gets no jar, so deploys
// that never used the agent behave exactly as before.

// verifyAll verifies what host's deploy pushes and returns the local
// files in upgrade order, the tracer first, with the tracer's signed
// version. Nothing is pushed unless all of them verified.
func (d deployer) verifyAll(arch string, projects []federate.Project) ([]string, string, error) {
	bin, version, err := d.verify(arch)
	if err != nil {
		return nil, "", err
	}
	files := []string{bin}
	if p := agentProject(projects); p != "" {
		jar, err := d.verifyAgent(version)
		if err != nil {
			return nil, "", fmt.Errorf("project %s uses the Java agent: %w", p, err)
		}
		files = append(files, jar)
	}
	return files, version, nil
}

// agentProject names the first project that uses the Java agent — a
// -spool (its JVMs' -javaagent needs the installed jar) or an -agent — or
// "" when none does.
func agentProject(projects []federate.Project) string {
	for _, p := range projects {
		for _, f := range p.Flags {
			key, _, _ := strings.Cut(strings.TrimLeft(f, "-"), "=")
			if strings.HasPrefix(f, "-") && (key == "spool" || key == "agent") {
				return p.Name
			}
		}
	}
	return ""
}

// verifyAgent checks dist/iormesh-agent.jar against the pinned key and
// requires it signed at the tracer's version: the server refuses any
// other, so a mismatch fails here, before the push, with the reason.
func (d deployer) verifyAgent(version string) (string, error) {
	jar := filepath.Join(d.dist, release.AgentFile)
	m, err := release.VerifyFile(d.key, jar)
	if err != nil {
		return "", fmt.Errorf("local build, nothing pushed: %w", err)
	}
	if m.Version != version {
		return "", fmt.Errorf("local build, nothing pushed: %s is signed as %s, the tracer as %s "+
			"(mage agentBuild, then mage sign, signs both)", jar, m.Version, version)
	}
	printVerified(d.e.stdout, jar, m)
	return jar, nil
}

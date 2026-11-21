package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"iormesh/internal/release"
	"iormesh/internal/target"
)

// The Java agent in the run (task kf4). M4-8 and M4-9 check the TLS
// label, which only the agent's http records give (internal/servicedb:
// the TLS hint is the requests' scheme), so the microservices projects
// trace with the agent, as the rig's TestMicroservices does:
//
//   - their registry entries carry -spool (the confined spool of the
//     project) and -agent (the jar deploy installs), so deploy ships and
//     installs the signed dist/iormesh-agent.jar, verified on the
//     controller and again by `iormesh upgrade` on the server;
//   - their JVMs start after the sessions, matched at exec, which a
//     session's -agent does not reach (it live-loads only JVMs running at
//     start; docs/java-attach.md), so the runner launches them with
//     JAVA_TOOL_OPTIONS=-javaagent:<installed jar>=spool=<spool>: the
//     same verified jar, loaded from the JVM's first instruction. The
//     environment, not a -javaagent argument, keeps the JVM's command
//     line "java -jar …", which the sessions' patterns match.
const (
	// installedAgent is where `iormesh upgrade` puts the agent jar, the
	// only -agent a deployed session may name.
	installedAgent = "/opt/iormesh/lib/" + release.AgentFile
	// spoolRoot holds the confined sessions' spools, one per project.
	spoolRoot = "/run/iormesh/spool"
)

// agentFlags are the registry flags of a project traced with the agent.
func agentFlags(proj string) []string {
	return []string{"-spool", spoolOf(proj), "-agent", installedAgent}
}

// spoolOf is a project's confined spool, the only one its session may
// use (the tracer pins it, deploy refuses another).
func spoolOf(proj string) string { return spoolRoot + "/" + proj }

// agentEnv is the environment a workload command needs on host: the
// -javaagent of the agent project whose pattern matches the command, as
// the session will see it, or nil when no agent project traces it. Two
// agent projects matching one command would be a configuration error;
// the first one wins, and the patterns' --dir suffixes rule it out.
func (r *Runner) agentEnv(host string, java []string) ([]string, error) {
	cmdline := strings.Join(java, " ")
	for _, p := range r.cfg.projects() {
		if !p.agent || p.host != host {
			continue
		}
		g, err := target.CompileGlob(p.pattern)
		if err != nil {
			return nil, fmt.Errorf("project %s: pattern %q: %w", p.name, p.pattern, err)
		}
		if g.Match(cmdline) {
			return []string{"env", "JAVA_TOOL_OPTIONS=-javaagent:" + installedAgent + "=spool=" +
				spoolOf(p.name)}, nil
		}
	}
	return nil, nil
}

// checkAgentBuild requires the signed agent jar in dist/: deploy ships it
// to both hosts (the microservices run on both) and refuses the host
// without it, so its absence is a preflight failure, not a half-deploy.
func (r *Runner) checkAgentBuild() error {
	jar := filepath.Join(r.cfg.Dist, release.AgentFile)
	for _, f := range []string{jar, jar + release.SigSuffix} {
		if _, err := os.Stat(f); err != nil {
			return fmt.Errorf("signed agent jar: %w (mage agentBuild && mage sign)", err)
		}
	}
	return nil
}

package integrationtests

import "testing"

// TestBuildHint: a missing artifact names the target that builds it (task
// yk4: the agent jar is mage agentBuild's, not workloadBuild's), and a
// missing source file gets no build advice.
func TestBuildHint(t *testing.T) {
	for rel, want := range map[string]string{
		agentJarDefault:     " (build it: mage agentBuild)",
		workloadJarDefault:  " (build it: mage workloadBuild)",
		edgePeerDefault:     " (build it: mage workloadBuild)",
		otelJarDefault:      " (build it: mage workloadOtelBuild)",
		libsJarDefault:      " (build it: mage workloadLibsBuild)",
		tracerBinaryDefault: " (build it: mage build)",
		ctlBinaryDefault:    " (build it: mage build)",
		evalQuestions:       "",
	} {
		if got := buildHint(rel); got != want {
			t.Errorf("buildHint(%q) = %q, want %q", rel, got, want)
		}
	}
}

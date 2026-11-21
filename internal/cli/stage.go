package cli

import (
	"errors"
	"slices"

	"iormesh/internal/javaattach"
)

// agentStage is the agent jar and a spool staged inside the root of a JVM
// in another mount namespace (decision #14, M7 step 3, task cf4);
// *javaattach.Stage implements it.
type agentStage interface {
	// Jar and Spool are the paths as the JVM sees them, ReadPath the
	// spool as the tracer reads it (a held descriptor).
	Jar() string
	Spool() string
	ReadPath() string
	Remove() error
}

var _ agentStage = (*javaattach.Stage)(nil)

// stageAgent stages jar inside pid's root, or returns the stage of have
// that pid's JVM may share: javaattach decides, from the JVM's euid and
// its /tmp's identity (never from the mount namespace alone, whose
// processes may run as other users or in other roots), after running
// every check for pid. A variable like newAgentLoader, so the session's
// tests stage into no real JVM.
var stageAgent = func(pid uint32, jar string, have []agentStage) (agentStage, error) {
	reuse := make([]*javaattach.Stage, 0, len(have))
	for _, h := range have {
		if s, ok := h.(*javaattach.Stage); ok {
			reuse = append(reuse, s)
		}
	}
	s, err := javaattach.New().Stage(pid, jar, reuse)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// agentTarget is what one load needs: the jar and the spool as the JVM
// resolves them, and the spool as the tracer reads it ("" when it cannot:
// `iormesh attach` into another mount namespace, where the ring is not
// checked).
type agentTarget struct {
	jar, spool, ringDir string
}

// agentStages are the stages one `run -agent` made, each listed once
// however many JVMs share it, all removed when the session ends
// (javaOutput.close).
type agentStages struct {
	jar string
	all []agentStage
}

func newAgentStages(jar string) *agentStages {
	return &agentStages{jar: jar}
}

// target returns pid's agentTarget: the tracer's own jar and spool for a
// JVM in the tracer's mount namespace, as before; for any other JVM a
// stage inside its root, shared with an earlier JVM only when stageAgent
// says so.
func (st *agentStages) target(pid uint32, spool string) (agentTarget, error) {
	if sameMountNS(pid) {
		return agentTarget{jar: st.jar, spool: spool, ringDir: spool}, nil
	}
	s, err := stageAgent(pid, st.jar, st.all)
	if err != nil {
		return agentTarget{}, err
	}
	if !slices.Contains(st.all, s) {
		st.all = append(st.all, s)
	}
	return agentTarget{jar: s.Jar(), spool: s.Spool(), ringDir: s.ReadPath()}, nil
}

// stagingFailed reports whether err is a JVM no stage could be made for
// — its /tmp unusable or untrusted, or its PID namespace not the
// tracer's — where -javaagent is the way out (decision #14).
func stagingFailed(err error) bool {
	return errors.Is(err, javaattach.ErrNoStage) || errors.Is(err, javaattach.ErrUntrustedTmp) ||
		errors.Is(err, javaattach.ErrOtherPidNS)
}

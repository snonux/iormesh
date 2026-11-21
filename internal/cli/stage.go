package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

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

// stageAgent stages jar inside pid's root. A variable like newAgentLoader,
// so the session's tests stage into no real JVM.
var stageAgent = func(pid uint32, jar string) (agentStage, error) {
	s, err := javaattach.New().Stage(pid, jar)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// stageKey names what JVMs may share one stage: the same mount namespace
// (so the same /tmp) and the same user (whose ownership of / or /tmp the
// stage's trust check accepted). "" when unreadable: that JVM gets a
// stage of its own. A variable so tests need no real processes.
var stageKey = func(pid uint32) string {
	dir := filepath.Join("/proc", strconv.FormatUint(uint64(pid), 10))
	ns, err := os.Readlink(filepath.Join(dir, "ns", "mnt"))
	if err != nil {
		return ""
	}
	st, err := os.Stat(dir)
	if err != nil {
		return ""
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return ns + "/" + strconv.FormatUint(uint64(sys.Uid), 10)
}

// agentTarget is what one load needs: the jar and the spool as the JVM
// resolves them, and the spool as the tracer reads it ("" when it cannot:
// `iormesh attach` into another mount namespace, where the ring is not
// checked).
type agentTarget struct {
	jar, spool, ringDir string
}

// agentStages are the stages one `run -agent` made, at most one per
// stageKey, all removed when the session ends (javaOutput.close).
type agentStages struct {
	jar   string
	byKey map[string]agentStage
	all   []agentStage
}

func newAgentStages(jar string) *agentStages {
	return &agentStages{jar: jar, byKey: map[string]agentStage{}}
}

// target returns pid's agentTarget: the tracer's own jar and spool for a
// JVM in the tracer's mount namespace, as before; for any other JVM a
// stage inside its root, made on first use for its stageKey.
func (st *agentStages) target(pid uint32, spool string) (agentTarget, error) {
	if sameMountNS(pid) {
		return agentTarget{jar: st.jar, spool: spool, ringDir: spool}, nil
	}
	key := stageKey(pid)
	s, ok := st.byKey[key]
	if !ok || key == "" {
		var err error
		if s, err = stageAgent(pid, st.jar); err != nil {
			return agentTarget{}, err
		}
		st.all = append(st.all, s)
		if key != "" {
			st.byKey[key] = s
		}
	}
	return agentTarget{jar: s.Jar(), spool: s.Spool(), ringDir: s.ReadPath()}, nil
}

// stagingFailed reports whether err is a stage that could not be made in
// the JVM's root, where -javaagent is the way out (decision #14).
func stagingFailed(err error) bool {
	return errors.Is(err, javaattach.ErrNoStage) || errors.Is(err, javaattach.ErrUntrustedTmp)
}

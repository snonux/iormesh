package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"iormesh/internal/javaattach"
)

// agentLoader live-loads the Java agent into one JVM (decision #2's
// live-attach path, task ge4); javaattach.Attacher implements it.
type agentLoader interface {
	Load(ctx context.Context, r javaattach.Request) error
}

var _ agentLoader = javaattach.Attacher{}

// newAgentLoader returns the host's attacher. A variable like
// processAlive, so the session's tests load into no real JVM.
var newAgentLoader = func() agentLoader { return javaattach.New() }

// attachConfig is a validated `iormesh attach` command line.
type attachConfig struct {
	pid uint32
	// agent and spool are absolute: the JVM resolves them, from its own
	// working directory.
	agent   string
	spool   string
	timeout time.Duration
}

// attachCommand is `iormesh attach`: it loads the agent into one running
// JVM, writing its ring into the spool of a `run -spool` already tracing
// it. Unlike `run -agent`, it attaches whenever the user (or a test) says
// so — the I/O the JVM did before stays unattributed.
func attachCommand(args []string, stderr io.Writer) error {
	cfg, err := parseAttachFlags(args, stderr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()
	// attach stages nothing: its spool belongs to a separate `run`, which
	// would never read a stage made here. A JVM in another mount namespace
	// must see both paths as they are; `run -agent` stages them instead.
	t := agentTarget{jar: cfg.agent, spool: cfg.spool}
	if sameMountNS(cfg.pid) {
		t.ringDir = cfg.spool
	}
	note, err := loadAgent(ctx, newAgentLoader(), cfg.pid, t)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "iormesh: %s\n", note)
	return nil
}

// parseAttachFlags parses and validates `attach`'s arguments. Every error
// wraps errUsage. The spool must exist already: run creates it with its
// mode and owner, and an agent writing elsewhere would be read by no one.
func parseAttachFlags(args []string, stderr io.Writer) (attachConfig, error) {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pid := fs.Uint("pid", 0, "the running JVM to load the agent into")
	agent := fs.String("agent", "", "path of iormesh-agent.jar")
	spool := fs.String("spool", "", "the -spool directory of the run tracing the JVM")
	timeout := fs.Duration("timeout", javaattach.DefaultTimeout, "give up on the attach after this long")
	if err := fs.Parse(args); err != nil {
		return attachConfig{}, fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() != 0 {
		return attachConfig{}, fmt.Errorf("%w: unexpected argument %q", errUsage, fs.Arg(0))
	}
	if *pid == 0 || *pid > math.MaxUint32 || !processAlive(uint32(*pid), 0) {
		return attachConfig{}, fmt.Errorf("%w: -pid %d: no such process", errUsage, *pid)
	}
	if *timeout <= 0 {
		return attachConfig{}, fmt.Errorf("%w: -timeout %v: must be positive", errUsage, *timeout)
	}
	cfg := attachConfig{pid: uint32(*pid), timeout: *timeout}
	var err error
	if cfg.agent, cfg.spool, err = agentPaths(*agent, *spool); err != nil {
		return attachConfig{}, err
	}
	if st, err := os.Stat(cfg.spool); err != nil || !st.IsDir() {
		return attachConfig{}, fmt.Errorf("%w: -spool %s: not a directory (`iormesh run -spool` creates it)",
			errUsage, cfg.spool)
	}
	return cfg, nil
}

// agentPaths makes the agent jar and the spool absolute and checks what
// the agent's option syntax can carry: its options are comma-separated,
// so a spool path with a comma would be cut.
func agentPaths(agent, spool string) (string, string, error) {
	if agent == "" || spool == "" {
		return "", "", fmt.Errorf("%w: -agent needs -spool, and both a path", errUsage)
	}
	absAgent, err := trustedAgent(agent)
	if err != nil {
		return "", "", fmt.Errorf("%w: -agent %s: %v", errUsage, agent, err)
	}
	absSpool, err := filepath.Abs(spool)
	if err != nil {
		return "", "", fmt.Errorf("%w: -spool: %v", errUsage, err)
	}
	if strings.Contains(absSpool, ",") {
		return "", "", fmt.Errorf("%w: -spool %s: the agent's options cannot carry a comma", errUsage, absSpool)
	}
	return absAgent, absSpool, nil
}

// trustedAgent resolves the agent jar to its real absolute path and
// checks that nobody but root and the caller can change what it is: the
// tracer, root, loads it into JVMs of every user, root's own included,
// so a jar another user could rewrite or swap would run their code in
// those JVMs. The jar and every directory above it must be owned by root
// or the caller and not writable by group or others — a directory with
// the sticky bit (/tmp) excepted, since nobody else may rename or remove
// the caller's entry there. The resolved path is what the JVM gets, so a
// symlink cannot be re-pointed after the check.
func trustedAgent(agent string) (string, error) {
	abs, err := filepath.Abs(agent)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(resolved); err != nil || !st.Mode().IsRegular() {
		return "", errors.New("not a file")
	}
	euid := uint32(os.Geteuid())
	for p := resolved; ; p = filepath.Dir(p) {
		var st unix.Stat_t
		if err := unix.Lstat(p, &st); err != nil {
			return "", err
		}
		if st.Uid != 0 && st.Uid != euid {
			return "", fmt.Errorf("%s belongs to uid %d, neither root nor the caller", p, st.Uid)
		}
		sticky := st.Mode&unix.S_IFMT == unix.S_IFDIR && st.Mode&unix.S_ISVTX != 0
		if st.Mode&0o022 != 0 && !sticky {
			return "", fmt.Errorf("%s is writable by group or others", p)
		}
		if p == "/" {
			return resolved, nil
		}
	}
}

// loadAgent loads the agent into pid with t's jar and spool and returns
// the line to report. The JVM confirms only that agentmain returned; the
// agent reports its own failures (an unsupported JDK, an unwritable
// spool) on the JVM's stderr and installs nothing, so the ring's
// appearance is checked too, in t.ringDir — the tracer's spool, or a
// stage's held descriptor; with no ringDir (attach into another mount
// namespace) it cannot be, which the line says, naming the ways that can.
// The ring is named by the pid as the JVM sees it, the tracer's own
// number in the shared PID namespace (staging refuses any other).
func loadAgent(ctx context.Context, l agentLoader, pid uint32, t agentTarget) (string, error) {
	err := l.Load(ctx, javaattach.Request{PID: pid, Jar: t.jar, Options: "spool=" + t.spool})
	if err != nil {
		return "", err
	}
	name := strconv.FormatUint(uint64(pid), 10) + ".ring"
	if t.ringDir == "" {
		return fmt.Sprintf("agent loaded into pid %d, but its ring could not be verified: the JVM is in "+
			"another mount namespace and resolves %s there (`run -agent` stages the jar and spool "+
			"inside its root; or start it with -javaagent)", pid, t.spool), nil
	}
	// Lstat and a regular file only: the spool is writable by every
	// target user, and a symlink planted as the ring must not count (nor
	// be followed) — Dir rejects it at its scan anyway.
	if st, err := os.Lstat(filepath.Join(t.ringDir, name)); err != nil || !st.Mode().IsRegular() {
		return fmt.Sprintf("agent loaded into pid %d, but it wrote no ring %s: no Java attribution "+
			"(its reason, iormesh-agent: …, is on the JVM's stderr)", pid, filepath.Join(t.spool, name)), nil
	}
	// JEP 451: the JVM itself prints the warning, on its own stderr.
	return fmt.Sprintf("agent loaded into pid %d; its I/O before now stays unattributed, and unless "+
		"it runs with -XX:+EnableDynamicAgentLoading the JVM printed JEP 451's warning on its stderr",
		pid), nil
}

// sameMountNS reports whether pid shares the tracer's mount namespace —
// whether the tracer's paths mean the same to it — or needs a stage
// inside its root (stage.go). A variable so tests need no real process;
// an unreadable namespace link counts as not shared.
var sameMountNS = func(pid uint32) bool {
	self, err := os.Readlink("/proc/self/ns/mnt")
	if err != nil {
		return false
	}
	other, err := os.Readlink(filepath.Join("/proc", strconv.FormatUint(uint64(pid), 10), "ns", "mnt"))
	return err == nil && other == self
}

// startJava readies the Java join before the ready file promises the
// tracer is live: the rings of JVMs already running get their natives
// probed (the Java mark, task td4), and with -agent the JVMs the session
// starts with get the agent loaded first (task ge4). Without -spool it
// does nothing.
func (s *session) startJava(ctx context.Context, cfg runConfig, stderr io.Writer) error {
	if cfg.agent != "" {
		return s.attachAgents(ctx, cfg.agent, cfg.spool, stderr)
	}
	return s.java.poll()
}

// attachAgents is `run -agent`: it loads the agent into each JVM the
// session starts with, while the main goroutine keeps handling records —
// a load takes S1's ≈150 ms or more, the JVM's own class loading is
// traced meanwhile, and a stalled reader would lose records — then reads
// the new rings, so their natives are probed before the ready file says
// the tracer is live. A JVM in another mount namespace gets the jar and
// a spool staged inside its root (decision #14), which the Java join
// reads beside -spool and removes when the session ends. A process that
// is no JVM, refuses the load or has no root to stage in is reported and
// traced on without Java attribution: decision #2 reports a refusal
// instead of retrying. However the loop ends — loads done, a signal, a
// failed record or a closed stream — the stages made so far reach the
// join, so the session removes them from the JVMs' roots (task ri4).
func (s *session) attachAgents(ctx context.Context, agent, spool string, stderr io.Writer) error {
	pids := make([]uint32, 0, len(s.live))
	for pid := range s.live {
		pids = append(pids, pid)
	}
	slices.Sort(pids)
	// The loads run on their own ctx, so the loop can cancel them when it
	// gives up on an error, not only when the session's ctx ends.
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan agentLoads, 1)
	go func() { done <- loadAgents(lctx, newAgentLoader(), pids, agent, spool) }()
	for {
		select {
		case batch, ok := <-s.tr.Records():
			if !ok {
				s.abandonLoads(cancel, done)
				return errors.New("record stream closed unexpectedly")
			}
			if err := s.handleAll(batch); err != nil {
				s.abandonLoads(cancel, done)
				return err
			}
		case r := <-done:
			for _, l := range r.lines {
				_, _ = fmt.Fprintf(stderr, "iormesh: %s\n", l)
			}
			s.java.addStages(r.stages)
			return s.java.poll()
		case <-ctx.Done():
			// A signal during the loads. nil, not ctx.Err(): traceLoop
			// sees the done ctx and ends the trace as stop, which an
			// error would turn into error.
			s.abandonLoads(cancel, done)
			return nil
		}
	}
}

// abandonLoads stops the loads attachAgents gave up on and hands their
// stages to the join, which removes them when the session ends. The
// loads end on the cancelled ctx, and loadAgents stages nothing more
// once it is done, so the wait is short; it is needed for the stages
// already made — and one still being made — which would otherwise stay
// in the JVMs' roots. Their lines are dropped: the session is ending.
func (s *session) abandonLoads(cancel context.CancelFunc, done <-chan agentLoads) {
	cancel()
	s.java.addStages((<-done).stages)
}

// agentLoads is what loadAgents did: one line per pid, and the stages it
// made inside JVMs' roots (decision #14), for the session to read and
// remove.
type agentLoads struct {
	lines  []string
	stages []agentStage
}

// loadAgents loads the agent into each pid in turn, each attach bounded
// by javaattach.DefaultTimeout, with the tracer's jar and spool or, for a
// JVM in another mount namespace, a stage inside its root. It runs beside
// the record loop, so it reports instead of writing stderr, and stops
// once ctx is done.
func loadAgents(ctx context.Context, l agentLoader, pids []uint32, agent, spool string) agentLoads {
	var lines []string
	st := newAgentStages(agent)
	for _, pid := range pids {
		if ctx.Err() != nil {
			break
		}
		note, err := loadOne(ctx, l, st, pid, spool)
		switch {
		case errors.Is(err, javaattach.ErrNotJVM):
			note = fmt.Sprintf("pid %d is not a JVM, no agent loaded", pid)
		case stagingFailed(err):
			note = fmt.Sprintf("no Java attribution for pid %d: %v (start it with -javaagent instead)",
				pid, err)
		case err != nil:
			note = fmt.Sprintf("no Java attribution for pid %d: %v", pid, err)
		}
		lines = append(lines, note)
	}
	return agentLoads{lines: lines, stages: st.all}
}

// loadOne stages (if pid needs it) and loads the agent into one JVM.
func loadOne(ctx context.Context, l agentLoader, st *agentStages, pid uint32, spool string) (string, error) {
	t, err := st.target(pid, spool)
	if err != nil {
		return "", err
	}
	actx, cancel := context.WithTimeout(ctx, javaattach.DefaultTimeout)
	defer cancel()
	return loadAgent(actx, l, pid, t)
}

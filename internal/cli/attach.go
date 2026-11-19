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
	note, err := loadAgent(ctx, newAgentLoader(), cfg.pid, cfg.agent, cfg.spool)
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
	absAgent, err := filepath.Abs(agent)
	if err != nil {
		return "", "", fmt.Errorf("%w: -agent: %v", errUsage, err)
	}
	if st, err := os.Stat(absAgent); err != nil || !st.Mode().IsRegular() {
		return "", "", fmt.Errorf("%w: -agent %s: not a file", errUsage, absAgent)
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

// loadAgent loads the agent into pid with its ring in spool and returns
// the line to report. The JVM confirms only that agentmain returned; the
// agent reports its own failures (an unsupported JDK, an unwritable
// spool) on the JVM's stderr and installs nothing, so the ring's
// appearance is checked too — only for a JVM in the tracer's mount
// namespace, where the spool path and the pid in the ring's name mean
// the same as here (containers are M7's).
func loadAgent(ctx context.Context, l agentLoader, pid uint32, agent, spool string) (string, error) {
	err := l.Load(ctx, javaattach.Request{PID: pid, Jar: agent, Options: "spool=" + spool})
	if err != nil {
		return "", err
	}
	ring := filepath.Join(spool, strconv.FormatUint(uint64(pid), 10)+".ring")
	if _, err := os.Stat(ring); err != nil && sameMountNS(pid) {
		return fmt.Sprintf("agent loaded into pid %d, but it wrote no ring %s: no Java attribution "+
			"(its reason, iormesh-agent: …, is on the JVM's stderr)", pid, ring), nil
	}
	// JEP 451: the JVM itself prints the warning, on its own stderr.
	return fmt.Sprintf("agent loaded into pid %d; its I/O before now stays unattributed, and unless "+
		"it runs with -XX:+EnableDynamicAgentLoading the JVM printed JEP 451's warning on its stderr",
		pid), nil
}

// sameMountNS reports whether pid shares the tracer's mount namespace. A
// variable so tests need no real process; an unreadable namespace link
// counts as not shared.
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
// the tracer is live. A process that is no JVM, or refuses the load, is
// reported and traced on without Java attribution: decision #2 reports a
// refusal instead of retrying.
func (s *session) attachAgents(ctx context.Context, agent, spool string, stderr io.Writer) error {
	pids := make([]uint32, 0, len(s.live))
	for pid := range s.live {
		pids = append(pids, pid)
	}
	slices.Sort(pids)
	notes := make(chan []string, 1)
	go func() { notes <- loadAgents(ctx, newAgentLoader(), pids, agent, spool) }()
	for {
		select {
		case batch, ok := <-s.tr.Records():
			if !ok {
				return errors.New("record stream closed unexpectedly")
			}
			if err := s.handleAll(batch); err != nil {
				return err
			}
		case lines := <-notes:
			for _, l := range lines {
				_, _ = fmt.Fprintf(stderr, "iormesh: %s\n", l)
			}
			return s.java.poll()
		}
	}
}

// loadAgents loads the agent into each pid in turn, each attach bounded
// by javaattach.DefaultTimeout, and returns one line per pid. It runs
// beside the record loop, so it reports instead of writing stderr.
func loadAgents(ctx context.Context, l agentLoader, pids []uint32, agent, spool string) []string {
	var lines []string
	for _, pid := range pids {
		actx, cancel := context.WithTimeout(ctx, javaattach.DefaultTimeout)
		note, err := loadAgent(actx, l, pid, agent, spool)
		cancel()
		switch {
		case errors.Is(err, javaattach.ErrNotJVM):
			note = fmt.Sprintf("pid %d is not a JVM, no agent loaded", pid)
		case err != nil:
			note = fmt.Sprintf("no Java attribution for pid %d: %v", pid, err)
		}
		lines = append(lines, note)
	}
	return lines
}

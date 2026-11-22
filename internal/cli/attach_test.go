package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"iormesh/internal/javaattach"
	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// fakeLoader records the loads asked of it and answers per pid.
type fakeLoader struct {
	errs   map[uint32]error
	got    []javaattach.Request
	during func() // runs inside each Load
}

func (f *fakeLoader) Load(_ context.Context, r javaattach.Request) error {
	f.got = append(f.got, r)
	if f.during != nil {
		f.during()
	}
	return f.errs[r.PID]
}

// useLoader makes l the loader newAgentLoader returns, and every pid
// share the tracer's mount namespace.
func useLoader(t *testing.T, l agentLoader) {
	t.Helper()
	origLoader, origNS := newAgentLoader, sameMountNS
	newAgentLoader = func() agentLoader { return l }
	sameMountNS = func(uint32) bool { return true }
	t.Cleanup(func() { newAgentLoader, sameMountNS = origLoader, origNS })
}

// agentFiles creates an agent jar and a spool directory to point at.
func agentFiles(t *testing.T) (agent, spool string) {
	t.Helper()
	dir := t.TempDir()
	agent, spool = filepath.Join(dir, "iormesh-agent.jar"), filepath.Join(dir, "spool")
	if err := os.WriteFile(agent, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(spool, 0o755); err != nil {
		t.Fatal(err)
	}
	return agent, spool
}

func TestParseAttachFlags(t *testing.T) {
	fakeProcs(t, 10)
	agent, spool := agentFiles(t)
	cfg, err := parseAttachFlags([]string{"-pid", "10", "-agent", agent, "-spool", spool}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	want := attachConfig{pid: 10, agent: agent, spool: spool, timeout: javaattach.DefaultTimeout}
	if cfg != want {
		t.Errorf("parseAttachFlags = %+v, want %+v", cfg, want)
	}
	comma := filepath.Join(filepath.Dir(spool), "a,b")
	if err := os.Mkdir(comma, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := map[string][]string{
		"no pid":        {"-agent", agent, "-spool", spool},
		"dead pid":      {"-pid", "11", "-agent", agent, "-spool", spool},
		"no agent":      {"-pid", "10", "-spool", spool},
		"missing jar":   {"-pid", "10", "-agent", agent + ".x", "-spool", spool},
		"no spool":      {"-pid", "10", "-agent", agent},
		"spool missing": {"-pid", "10", "-agent", agent, "-spool", spool + ".x"},
		"spool comma":   {"-pid", "10", "-agent", agent, "-spool", comma},
		"zero timeout":  {"-pid", "10", "-agent", agent, "-spool", spool, "-timeout", "0s"},
		"extra arg":     {"-pid", "10", "-agent", agent, "-spool", spool, "x"},
	}
	for name, args := range bad {
		if _, err := parseAttachFlags(args, &bytes.Buffer{}); !errors.Is(err, errUsage) {
			t.Errorf("%s: err = %v, want a usage error", name, err)
		}
	}
}

// TestRunAgentFlag: -agent makes both paths absolute and needs -spool.
func TestRunAgentFlag(t *testing.T) {
	fakeProcs(t, 10)
	agent, spool := agentFiles(t)
	t.Chdir(filepath.Dir(agent))
	base := []string{"-pid", "10", "-project", "p", "-out", "/tmp/t"}
	cfg, err := parseRunFlags(append(base, "-spool", "spool", "-agent", "iormesh-agent.jar"), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.agent != agent || cfg.spool != spool {
		t.Errorf("agent %q, spool %q; want %q, %q", cfg.agent, cfg.spool, agent, spool)
	}
	if _, err := parseRunFlags(append(base, "-agent", agent), &bytes.Buffer{}); !errors.Is(err, errUsage) {
		t.Errorf("-agent without -spool: err = %v, want a usage error", err)
	}
}

// TestLoadAgentChecksRing: a load the JVM confirmed is reported as such
// only when the agent's ring appeared; a refusal is the loader's error.
func TestLoadAgentChecksRing(t *testing.T) {
	useLoader(t, nil)
	agent, spool := agentFiles(t)
	l := &fakeLoader{errs: map[uint32]error{30: javaattach.ErrDynamicLoadingDisabled}}
	note, err := loadAgent(context.Background(), l, 10, agentTarget{agent, spool, spool})
	if err != nil || !strings.Contains(note, "wrote no ring") {
		t.Errorf("no ring: %q, %v", note, err)
	}
	if err := os.WriteFile(filepath.Join(spool, "10.ring"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	note, err = loadAgent(context.Background(), l, 10, agentTarget{agent, spool, spool})
	if err != nil || !strings.Contains(note, "agent loaded into pid 10;") {
		t.Errorf("ring: %q, %v", note, err)
	}
	if _, err := loadAgent(context.Background(), l, 30, agentTarget{agent, spool, spool}); !errors.Is(err, javaattach.ErrDynamicLoadingDisabled) {
		t.Errorf("refused load: err = %v", err)
	}
	if want := (javaattach.Request{PID: 10, Jar: agent, Options: "spool=" + spool}); l.got[0] != want {
		t.Errorf("request %+v, want %+v", l.got[0], want)
	}
}

// TestAttachAgentsHandlesRecords: `run -agent` keeps consuming records
// while a load is in flight — the loader waits for its batch to be taken
// — and reports every target: loaded, no JVM, or refused.
func TestAttachAgentsHandlesRecords(t *testing.T) {
	fakeProcs(t, 10, 20, 30)
	agent, spool := agentFiles(t)
	tr := newFakeTracer()
	l := &fakeLoader{errs: map[uint32]error{20: javaattach.ErrNotJVM,
		30: javaattach.ErrDynamicLoadingDisabled}}
	l.during = func() {
		tr.records <- []any{}
		deadline := time.Now().Add(5 * time.Second)
		for len(tr.records) > 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if len(tr.records) > 0 {
			t.Error("records were not handled during the load")
		}
	}
	useLoader(t, l)
	c := testCorrelator(t, t.TempDir())
	defer func() { _ = c.Close() }()
	s, _ := pidSession(t, tr, c, 10, 20, 30)
	var stderr bytes.Buffer
	if err := s.attachAgents(context.Background(), agent, spool, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"agent loaded into pid 10, but it wrote no ring",
		"pid 20 is not a JVM", "no Java attribution for pid 30: dynamic agent loading is disabled"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr.String())
		}
	}
	if len(l.got) != 3 {
		t.Errorf("%d loads, want 3", len(l.got))
	}
}

// blockingLoader hands the record loop a batch on its first load (fail
// says how: a batch the ended correlator refuses, or a closed stream),
// then blocks until its ctx is done, as a real attach would until its
// timeout: the stage made for that load is still in flight when the
// loop gives up.
type blockingLoader struct {
	tr    *fakeTracer
	fail  func(*fakeTracer)
	loads int
}

func (b *blockingLoader) Load(ctx context.Context, _ javaattach.Request) error {
	b.loads++
	if b.loads == 1 {
		b.fail(b.tr)
	}
	<-ctx.Done()
	return ctx.Err()
}

// TestAttachAgentsRemovesStagesOnError: when a record fails, or the
// stream closes, while `run -agent` is still loading, the loads are
// cancelled and every stage made inside a JVM's root is removed, not
// left in its /tmp; no JVM after the failure is staged.
func TestAttachAgentsRemovesStagesOnError(t *testing.T) {
	fails := map[string]func(*fakeTracer){
		"record fails":  func(tr *fakeTracer) { tr.records <- []any{&types.FdEvent{Pid: 20, Tid: 20}} },
		"stream closed": func(tr *fakeTracer) { close(tr.records) },
	}
	for name, fail := range fails {
		t.Run(name, func(t *testing.T) {
			fakeProcs(t, 20, 21)
			agent, spool := agentFiles(t)
			tr := newFakeTracer()
			l := &blockingLoader{tr: tr, fail: fail}
			useLoader(t, l)
			stages := useStages(t, agent, nil)
			c := testCorrelator(t, t.TempDir())
			defer func() { _ = c.Close() }()
			s, _ := pidSession(t, tr, c, 20, 21)
			if err := c.End(schema.EndStop); err != nil {
				t.Fatal(err)
			}
			// Cancelled at cleanup, so a loader the loop abandoned ends.
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			if err := s.attachAgents(ctx, agent, spool, &bytes.Buffer{}); err == nil {
				t.Fatal("attachAgents succeeded, want the record loop's error")
			}
			if err := s.java.close(); err != nil {
				t.Fatal(err)
			}
			if len(*stages) != 1 {
				t.Fatalf("%d stages made, want 1 (pid 21 not staged after the failure)", len(*stages))
			}
			if r := (*stages)[0].removed; r != 1 {
				t.Errorf("stage %s removed %d times, want once", (*stages)[0].name, r)
			}
		})
	}
}

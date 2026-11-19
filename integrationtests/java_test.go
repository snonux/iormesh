package integrationtests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"iormesh/internal/normalize"
	"iormesh/internal/schema"
)

// agentJarDefault is `mage agentBuild`'s output, which `mage
// integrationTest` builds first.
const agentJarDefault = "../iormesh-agent.jar"

// TestJavaMethodFlow: method-flow under the agent, traced with -spool,
// carries the Java side its pending golden declares (M3, task 2b4): each
// declared I/O op is attribution java, on the declared Java thread, caused
// by the declared method — the innermost frame outside the JDK
// (normalize.CausingMethod). Its golden was expect-pending.json until
// task 3b4 promoted it.
func TestJavaMethodFlow(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	h.JavaAgent = absPath(t, agentJarDefault)
	h.Spool = filepath.Join(h.Dir, "spool")
	run := traceJudged(t, h, startScenario("method-flow"))
	assertOnlyTracedPIDs(t, run.Trace.Events, run.pids)
	// One ring read; a retried attempt may also report the earlier
	// attempt's ring as skipped (its JVM is gone, its owner unverifiable).
	if !strings.Contains(run.Logged, "java rings 1 (") ||
		!strings.Contains(run.Logged, ", corrupt 0, unmarked JVMs 0)") {
		t.Errorf("summary does not report the one ring read: %s", run.Logged)
	}
	assertJavaConsistent(t, run.Trace)
	assertClassesMapToJar(t, run.Dir, run.Trace, h.WorkloadJar)

	got := normalized(t, h, run, "method-flow")
	want := expectation(t, "method-flow")
	assertGolden(t, got, want)

	t.Run("wrong expectations are caught", func(t *testing.T) {
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			"another method": func(x *normalize.Expectation) {
				x.Events[1].Java = &normalize.ExpectedJava{Thread: "main",
					Method: "iormesh.workload.scenarios.MethodFlow$Journal#create"}
			},
			"declared unattributed": func(x *normalize.Expectation) {
				x.Events[1].Attribution, x.Events[1].Java = schema.AttributionNone, nil
			},
			"another thread": func(x *normalize.Expectation) {
				j := *x.Events[1].Java
				j.Thread = "method-flow-worker"
				x.Events[1].Java = &j
			},
		})
	})
}

// assertJavaConsistent: every java stack id of the trace is in its method
// table, and only events with attribution java carry one.
func assertJavaConsistent(t *testing.T, tr normalize.Trace) {
	t.Helper()
	joined := 0
	for _, e := range tr.Events {
		if e.Attribution != schema.AttributionJava {
			continue
		}
		joined++
		for _, id := range e.Java.Stack {
			if _, ok := tr.Methods[id]; !ok {
				t.Errorf("event seq %d: method id %d not in methods.ndjson", e.Seq, id)
			}
		}
	}
	if joined == 0 {
		t.Error("no event joined to a Java stack")
	}
}

// assertClassesMapToJar: the class-load map (task fe4) joins every frame
// of the trace to its class, read strictly from classes.ndjson; each
// joined event's causing method (the scenario's own code) is a class the
// app loader read from the workload jar, and its JDK frames are classes
// of the runtime image.
func assertClassesMapToJar(t *testing.T, dir string, tr normalize.Trace, jar string) {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "classes.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	rows, err := schema.ReadTable[schema.Class](f)
	if err != nil {
		t.Fatal(err)
	}
	classes := map[uint64]schema.Class{}
	for _, c := range rows {
		classes[c.ClassID] = c
	}
	// The JVM resolves the class path's symlinks.
	jarPath, err := filepath.EvalSymlinks(jar)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range tr.Methods {
		c, ok := classes[m.ClassID]
		switch {
		case !ok:
			t.Errorf("method %d (%s): class %d not in classes.ndjson", m.MethodID, m.Frame, m.ClassID)
		case c.Class != m.Class:
			t.Errorf("method %d (%s): class row %+v", m.MethodID, m.Frame, c)
		}
	}
	causing := 0
	for _, e := range tr.Events {
		if e.Attribution != schema.AttributionJava {
			continue
		}
		for _, id := range e.Java.Stack {
			m := tr.Methods[id]
			c := classes[m.ClassID]
			if strings.HasPrefix(m.Class, "iormesh.workload.") {
				causing++
				if c.CodeSource != jarPath || c.Loader != "app" {
					t.Errorf("event seq %d: %s from %q (%s), want %s (app)", e.Seq, m.Class,
						c.CodeSource, c.Loader, jarPath)
				}
			} else if strings.HasPrefix(m.Class, "java.") && !strings.HasPrefix(c.CodeSource, "jrt:/") {
				t.Errorf("event seq %d: JDK class %s from %q", e.Seq, m.Class, c.CodeSource)
			}
		}
	}
	if causing == 0 {
		t.Error("no frame of the workload's own classes")
	}
}

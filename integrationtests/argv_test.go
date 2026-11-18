package integrationtests

// M2 step 8 end to end: a child JVM started with a planted secret in its
// argv is traced through its exec, and the secret is nowhere in what the
// tracer writes — not in events.ndjson, not in graph.json — while the
// exec event still carries the argv, masked (PLAN.md decision #9 and the
// M0 redaction expectation; docs/implementation.md M2 step 8).

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/normalize"
	"iormesh/internal/schema"
)

// argvSecret is the secret the argv-secret scenario plants
// (ArgvSecret.SECRET); its expect.json lists it under must_not_appear.
const argvSecret = "Hunter2PlantedArgvSecret"

// TestArgvSecretRedacted: the golden holds (exec event of the child,
// forked edge, must_not_appear), the raw trace files hold no trace of the
// secret, and the child JVM's exec event records every planted shape
// masked rather than dropped.
func TestArgvSecretRedacted(t *testing.T) {
	enableParallelIfRequested(t)
	h := newHarness(t)
	run := traceJudged(t, h, func(h *Harness) ([]*Workload, error) {
		w, err := h.StartOptionless("argv-secret")
		return []*Workload{w}, err
	})
	assertTracedOrForked(t, run.Trace, run.pids)
	for _, name := range []string{eventsFile, graphFile} {
		raw, err := os.ReadFile(filepath.Join(run.Dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), argvSecret) {
			t.Errorf("%s holds the planted argv secret", name)
		}
	}
	assertMaskedArgv(t, run.Trace.Events)
	got := normalized(t, h, run, "argv-secret")
	want := expectation(t, "argv-secret")
	assertGolden(t, got, want)
	t.Run("wrong expectations are caught", func(t *testing.T) {
		assertCaught(t, got, want, map[string]func(*normalize.Expectation){
			// The walker must reach argv: the masked form is in it.
			"the masked property is forbidden": func(x *normalize.Expectation) {
				x.MustNotAppear = []string{"-Dpassword=***"}
			},
		})
	})
}

// assertMaskedArgv finds the child JVM's exec (the one whose argv names
// ArgvChild) and checks each planted shape came out masked.
func assertMaskedArgv(t *testing.T, events []schema.Event) {
	t.Helper()
	for _, e := range events {
		if e.Event != schema.KindExec || !slices.Contains(e.Args.Argv, "iormesh.workload.scenarios.ArgvChild") {
			continue
		}
		argv := e.Args.Argv
		for _, want := range []string{"-Dpassword=***", "--api-key=***",
			"https://bob:***@example.invalid/x?sig=***"} {
			if !slices.Contains(argv, want) {
				t.Errorf("exec argv %q lacks %q", argv, want)
			}
		}
		if i := slices.Index(argv, "--token"); i < 0 || i+1 >= len(argv) || argv[i+1] != "***" {
			t.Errorf("exec argv %q: --token's value is not masked", argv)
		}
		return
	}
	t.Fatal("no exec event of the ArgvChild JVM: its argv was not recorded")
}

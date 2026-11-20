package eval

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// ErrOptIn is the refusal without a model command: the eval sends the
// rollup and query slices to it, and never picks one on its own.
var ErrOptIn = errors.New("no model command: the eval sends the trace's rollup and slices to the " +
	"model command, so it runs only when one is named explicitly (-model-cmd, or " +
	"IORMESH_EVAL_MODEL_CMD for mage eval); use a local model or a workload trace, never a " +
	"production one")

// Options are the command line of cmd/iormesheval.
type Options struct {
	Questions, Store, Iormeshctl, Source, Out string
	Projects                                  projectList
	// ModelCmd is the model command line, split as SplitArgs splits;
	// ModelName labels it in the results (the command may hold secrets).
	ModelCmd, ModelName string
	// Date names the results file (YYYY-MM-DD); default today, UTC.
	Date       string
	MaxTurns   int
	SliceBytes int64
	Timeout    time.Duration
}

// projectList is a repeatable -project flag.
type projectList []string

func (p *projectList) String() string     { return fmt.Sprint(*p) }
func (p *projectList) Set(v string) error { *p = append(*p, v); return nil }

// ParseFlags parses cmd/iormesheval's flags.
func ParseFlags(args []string, stderr io.Writer) (Options, error) {
	fs := flag.NewFlagSet("iormesheval", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o Options
	fs.StringVar(&o.Questions, "questions", "eval/questions/microservices.yaml", "question set")
	fs.StringVar(&o.Store, "store", "", "collected trace store holding the scenario's trace (required)")
	fs.Var(&o.Projects, "project", "project of the store to build (repeatable; default: all)")
	fs.StringVar(&o.Iormeshctl, "iormeshctl", "./iormeshctl", "iormeshctl binary")
	fs.StringVar(&o.Source, "source", "workload/src", "workload source root the model may read")
	fs.StringVar(&o.Out, "out", "eval/results", "results directory")
	fs.StringVar(&o.ModelCmd, "model-cmd", "", "model command: prompt on stdin, reply on stdout (required)")
	fs.StringVar(&o.ModelName, "model-name", "unnamed", "the model's label in the results")
	fs.StringVar(&o.Date, "date", time.Now().UTC().Format(time.DateOnly), "results date")
	fs.IntVar(&o.MaxTurns, "max-turns", DefaultMaxTurns, "turns per question")
	fs.Int64Var(&o.SliceBytes, "max-bytes", DefaultSliceBytes, "cap of one query slice")
	fs.DurationVar(&o.Timeout, "timeout", DefaultModelTimeout, "bound of one model reply")
	if err := fs.Parse(args); err != nil {
		return Options{}, err
	}
	if fs.NArg() != 0 {
		return Options{}, fmt.Errorf("iormesheval takes no arguments")
	}
	return o, o.validate()
}

// validate refuses what would make the run meaningless or unsafe:
// above all a missing model command (ErrOptIn).
func (o Options) validate() error {
	switch {
	case o.ModelCmd == "":
		return ErrOptIn
	case o.Store == "":
		return errors.New("-store is required: the collected store of the scenario's trace")
	case o.MaxTurns < 1:
		return fmt.Errorf("-max-turns %d: want at least 1", o.MaxTurns)
	case o.SliceBytes < 1024:
		return fmt.Errorf("-max-bytes %d: want at least 1024", o.SliceBytes)
	}
	if _, err := time.Parse(time.DateOnly, o.Date); err != nil {
		return fmt.Errorf("-date %q: want YYYY-MM-DD", o.Date)
	}
	return nil
}

// Main runs the eval: build the store into a scratch database and
// builds directory (never the operator's), read the rollup, ask every
// question, write the results and print the score. It fails when the
// score is below the set's threshold.
func Main(ctx context.Context, o Options, stdout io.Writer) error {
	set, err := LoadSet(o.Questions)
	if err != nil {
		return err
	}
	argv, err := SplitArgs(o.ModelCmd)
	if err != nil || len(argv) == 0 {
		return fmt.Errorf("-model-cmd %q: %v", o.ModelCmd, err)
	}
	work, err := os.MkdirTemp("", "iormesh-eval-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	s, err := newSession(ctx, o, set, work)
	if err != nil {
		return err
	}
	s.Model = Command{Argv: argv, Timeout: o.Timeout}
	r := s.Run(ctx, o.Date, o.ModelName)
	if err := writeResult(o.Out, r); err != nil {
		return err
	}
	printResult(stdout, r)
	if !r.Passed {
		return fmt.Errorf("eval %s: score %.2f below the threshold %.2f", r.Scenario, r.Score, r.Threshold)
	}
	return nil
}

// newSession builds the store and gathers what the prompt carries.
func newSession(ctx context.Context, o Options, set Set, work string) (Session, error) {
	ctl := Ctl{Path: o.Iormeshctl, Store: o.Store, DB: filepath.Join(work, "services.db"),
		Builds: filepath.Join(work, "builds"), Projects: o.Projects, SliceBytes: o.SliceBytes}
	if err := ctl.Build(ctx); err != nil {
		return Session{}, err
	}
	rep, err := ctl.Report(ctx)
	if err != nil {
		return Session{}, err
	}
	tree := SourceTree{Root: o.Source, MaxBytes: DefaultSourceBytes}
	sources, err := tree.List()
	if err != nil {
		return Session{}, fmt.Errorf("source: %w", err)
	}
	return Session{Set: set, Report: rep, Sources: sources, Tools: Toolbox{Ctl: ctl, Tree: tree},
		MaxTurns: o.MaxTurns}, nil
}

// writeResult writes <out>/<date>-<scenario>.json.
func writeResult(out string, r Result) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, r.Date+"-"+r.Scenario+".json"), append(data, '\n'), 0o644)
}

// printResult prints one line per question and the score.
func printResult(w io.Writer, r Result) {
	for _, q := range r.Questions {
		mark := "PASS"
		if !q.Passed {
			mark = "FAIL"
		}
		_, _ = fmt.Fprintf(w, "%s %-22s %d turns %v\n", mark, q.ID, q.Turns, q.Failures)
	}
	_, _ = fmt.Fprintf(w, "score %.2f (threshold %.2f): %d questions, model %s\n", r.Score, r.Threshold,
		len(r.Questions), r.Model)
}

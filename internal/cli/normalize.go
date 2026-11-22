package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"iormesh/internal/normalize"
	"iormesh/internal/schema"
)

// normalizeConfig is a validated `iormesh normalize` command line.
type normalizeConfig struct {
	dir     string // trace directory (events.ndjson + graph.json)
	bracket string
	tmp     string
	check   string // expect.json; empty = write the normalized trace
	out     string // directory for the normalized trace; empty = stdout
}

func parseNormalizeFlags(args []string, stderr io.Writer) (normalizeConfig, error) {
	fs := flag.NewFlagSet("normalize", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfg normalizeConfig
	fs.StringVar(&cfg.bracket, "bracket", "", "cut to this scenario's marker bracket")
	fs.StringVar(&cfg.tmp, "tmp", "", "path prefix to replace with $tmp")
	fs.StringVar(&cfg.check, "check", "", "golden-check against this expect.json")
	fs.StringVar(&cfg.out, "out", "", "write the normalized trace into this directory")
	if err := fs.Parse(args); err != nil {
		return cfg, fmt.Errorf("%w: %w", errUsage, err)
	}
	if fs.NArg() != 1 {
		return cfg, fmt.Errorf("%w: want exactly one trace directory, got %d arguments", errUsage, fs.NArg())
	}
	cfg.dir = fs.Arg(0)
	if cfg.check != "" && cfg.out != "" {
		return cfg, fmt.Errorf("%w: -check and -out exclude each other", errUsage)
	}
	return cfg, nil
}

// normalizeCommand normalizes a trace, then either checks it against an
// expectation (violations → exit 3) or writes it out.
func normalizeCommand(args []string, stdout, stderr io.Writer) (int, error) {
	cfg, err := parseNormalizeFlags(args, stderr)
	if err != nil {
		return 0, err
	}
	trace, err := readTrace(cfg.dir)
	if err != nil {
		return 0, err
	}
	got, err := normalize.Normalize(trace, normalize.Options{Bracket: cfg.bracket, TmpPrefix: cfg.tmp})
	if err != nil {
		return 0, err
	}
	if cfg.check != "" {
		return checkTrace(got, cfg.check, stdout)
	}
	return exitOK, writeNormalized(got, cfg.out, stdout)
}

// readTrace reads a trace directory strictly.
func readTrace(dir string) (normalize.Trace, error) {
	f, err := os.Open(filepath.Join(dir, EventsFile))
	if err != nil {
		return normalize.Trace{}, err
	}
	defer func() { _ = f.Close() }()
	events, err := schema.ReadNDJSON(f)
	if err != nil {
		return normalize.Trace{}, fmt.Errorf("%s: %w", f.Name(), err)
	}
	data, err := os.ReadFile(filepath.Join(dir, GraphFile))
	if err != nil {
		return normalize.Trace{}, err
	}
	graph, err := schema.DecodeGraph(data)
	if err != nil {
		return normalize.Trace{}, fmt.Errorf("%s: %w", GraphFile, err)
	}
	methods, err := normalize.LoadMethods(filepath.Join(dir, MethodsFile))
	if err != nil {
		return normalize.Trace{}, err
	}
	return normalize.Trace{Events: events, Graph: graph, Methods: methods}, nil
}

func checkTrace(got normalize.Trace, path string, stdout io.Writer) (int, error) {
	want, err := normalize.LoadExpectation(path)
	if err != nil {
		return 0, err
	}
	violations := normalize.Check(got, want)
	for _, v := range violations {
		if _, err := fmt.Fprintln(stdout, v); err != nil {
			return 0, err
		}
	}
	if len(violations) > 0 {
		return exitViolations, nil
	}
	_, err = fmt.Fprintf(stdout, "ok: %d events match %s\n", len(got.Events), path)
	return exitOK, err
}

// writeNormalized writes the normalized trace into out, or its events to
// stdout when out is empty.
func writeNormalized(t normalize.Trace, out string, stdout io.Writer) error {
	if out == "" {
		return writeEvents(schema.NewNDJSONSink(nopCloser{stdout}), t.Events)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(out, EventsFile))
	if err != nil {
		return err
	}
	if err := writeEvents(schema.NewNDJSONSink(f), t.Events); err != nil {
		return err
	}
	return schema.WriteGraphFile(filepath.Join(out, GraphFile), t.Graph)
}

func writeEvents(s *schema.NDJSONSink, events []schema.Event) error {
	for i := range events {
		if err := s.Event(&events[i]); err != nil {
			_ = s.Close()
			return err
		}
	}
	return s.Close()
}

// nopCloser keeps the sink from closing stdout.
type nopCloser struct{ io.Writer }

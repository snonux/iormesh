package eval

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Defaults of the tools' caps.
const (
	// DefaultSliceBytes caps one query slice handed to the model: a
	// fraction of a context window, so several fit in one transcript.
	DefaultSliceBytes = 32 << 10
	// DefaultSourceBytes caps one source file handed to the model.
	DefaultSourceBytes = 32 << 10
	// maxSourceFiles caps the source listing in the prompt.
	maxSourceFiles = 200
)

// queryValueFlags and queryBoolFlags are the slice flags a model may
// pass to `iormeshctl query`: never -store, -db or -project (the
// harness fixes those) and never -max-bytes (the harness caps).
var (
	queryValueFlags = []string{"from", "to", "host", "app", "thread", "conn", "file", "method"}
	queryBoolFlags  = []string{"follow"}
)

// Ctl runs iormeshctl against the eval's store, with its own database
// and builds directory, so the eval never touches the operator's.
type Ctl struct {
	// Path is the iormeshctl binary.
	Path              string
	Store, DB, Builds string
	Projects          []string
	SliceBytes        int64
}

// paths are the flags every command shares.
func (c Ctl) paths() []string {
	args := []string{"-store", c.Store}
	for _, p := range c.Projects {
		args = append(args, "-project", p)
	}
	return args
}

// Build runs `iormeshctl build`. A build that left segments out exits 1
// with its outputs written: the eval then runs on a partial trace, so
// it fails instead.
func (c Ctl) Build(ctx context.Context) error {
	args := slices.Concat([]string{"build", "-db", c.DB, "-builds", c.Builds}, c.paths())
	out, err := c.run(ctx, args)
	if err != nil {
		return fmt.Errorf("iormeshctl build: %w\n%s", err, out)
	}
	return nil
}

// Report is `iormeshctl show report`: the run summary and the rollup.
func (c Ctl) Report(ctx context.Context) (string, error) {
	args := slices.Concat([]string{"show", "report", "-builds", c.Builds}, c.paths())
	out, err := c.run(ctx, args)
	if err != nil {
		return "", fmt.Errorf("iormeshctl show report: %w\n%s", err, out)
	}
	return out, nil
}

// Query runs `iormeshctl query` with the model's slice flags. A usage
// error or a refused flag is the model's to read, not the harness's to
// fail on: it comes back as the result text.
func (c Ctl) Query(ctx context.Context, flags []string) (string, error) {
	if err := CheckQueryArgs(flags); err != nil {
		return "error: " + err.Error(), nil
	}
	args := slices.Concat([]string{"query", "-db", c.DB}, c.paths(),
		[]string{"-max-bytes", strconv.FormatInt(c.SliceBytes, 10)}, flags)
	out, err := c.run(ctx, args)
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Sprintf("%serror: iormeshctl query exited %d", out, exit.ExitCode()), nil
	}
	return out, err
}

// run runs iormeshctl and returns stdout and stderr together.
func (c Ctl) run(ctx context.Context, args []string) (string, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, c.Path, args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// CheckQueryArgs accepts only the slice flags: -name value, -name=value
// or the boolean -follow; anything else — another flag, a positional
// argument — is refused.
func CheckQueryArgs(args []string) error {
	for i := 0; i < len(args); i++ {
		name, _, hasValue := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		switch {
		case !strings.HasPrefix(args[i], "-"):
			return fmt.Errorf("query takes flags only, not %q", args[i])
		case slices.Contains(queryBoolFlags, name):
		case !slices.Contains(queryValueFlags, name):
			return fmt.Errorf("flag -%s is not allowed (allowed: -%s, -%s)", name,
				strings.Join(queryValueFlags, ", -"), strings.Join(queryBoolFlags, ", -"))
		case !hasValue && i+1 >= len(args):
			return fmt.Errorf("flag -%s needs a value", name)
		case !hasValue:
			i++ // the next argument is the flag's value
		}
	}
	return nil
}

// SourceTree hands out the workload's source files, read only below
// Root.
type SourceTree struct {
	Root     string
	MaxBytes int64
}

// Source reads one file below the root through os.Root, so neither
// "..", an absolute path nor a symlink can leave it. A missing or
// refused file is the model's to read, as the result text.
func (s SourceTree) Source(path string) (string, error) {
	root, err := os.OpenRoot(s.Root)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(filepath.FromSlash(path))
	if err != nil {
		return "error: " + strings.ReplaceAll(err.Error(), s.Root, "<source>"), nil
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, s.MaxBytes+1))
	if err != nil {
		return "error: " + strings.ReplaceAll(err.Error(), s.Root, "<source>"), nil
	}
	if int64(len(data)) > s.MaxBytes {
		return string(data[:s.MaxBytes]) + fmt.Sprintf("\n[cut at %d bytes]\n", s.MaxBytes), nil
	}
	return string(data), nil
}

// List names the regular files below the root, sorted, slash-separated,
// at most maxSourceFiles.
func (s SourceTree) List() ([]string, error) {
	var out []string
	err := filepath.WalkDir(s.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		rel, err := filepath.Rel(s.Root, p)
		if err == nil {
			out = append(out, filepath.ToSlash(rel))
		}
		return err
	})
	slices.Sort(out)
	if len(out) > maxSourceFiles {
		out = out[:maxSourceFiles]
	}
	return out, err
}

// SplitArgs splits a QUERY line's flags as a shell would for the simple
// cases a model writes: whitespace separates, single or double quotes
// group (no escapes, no expansion: the result is never a shell's).
func SplitArgs(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	quote, inArg := rune(0), false
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote, inArg = r, true
		case r == ' ' || r == '\t':
			if inArg {
				out, inArg = append(out, cur.String()), false
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote")
	}
	if inArg {
		out = append(out, cur.String())
	}
	return out, nil
}

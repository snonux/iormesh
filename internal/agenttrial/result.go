package agenttrial

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"iormesh/internal/eval"
)

// Result is one trial run.
type Result struct {
	Agent string `json:"agent"`
	// Model is the model's name, never a command line.
	Model string `json:"model"`
	// Effort is claude's effort level (task ip4); results from before it,
	// and pi's, have none — claude's then ran at medium.
	Effort   string  `json:"effort,omitempty"`
	Mode     string  `json:"mode"`
	Date     string  `json:"date"`
	Commit   string  `json:"commit"`
	Turns    int     `json:"turns"`
	MaxTurns int     `json:"max_turns"`
	Minutes  float64 `json:"minutes"`
	// Mesh is the install-and-trace check: a built mesh with the
	// workload's cross-service edges; MeshError says why not.
	Mesh      bool    `json:"mesh"`
	MeshError string  `json:"mesh_error,omitempty"`
	Threshold float64 `json:"threshold"`
	Score     float64 `json:"score"`
	Passed    bool    `json:"passed"`
	// Failure is a rule the run broke (help, a command not approved, a
	// budget); any failure fails the run whatever its score.
	Failure   string   `json:"failure,omitempty"`
	Questions []Answer `json:"questions"`
	// Transcript and Commands name the run's files (gitignored).
	Transcript string `json:"transcript"`
	Commands   string `json:"commands"`
}

// Answer is one question's outcome, scored by M5's matchers
// (eval.Question.Score).
type Answer struct {
	ID       string   `json:"id"`
	Passed   bool     `json:"passed"`
	Answer   string   `json:"answer"`
	Failures []string `json:"failures"`
}

// finish scores the answers and runs the mesh check (#37: the pass is 8
// of 10, and only after the install and the trace succeeded).
func (t *Trial) finish(ctx context.Context, r *Result, answers map[string]string) {
	passed := 0
	for _, q := range t.Set.Questions {
		a := Answer{ID: q.ID, Answer: answers[q.ID], Failures: []string{}}
		if strings.TrimSpace(a.Answer) == "" {
			a.Failures = append(a.Failures, "no answer")
		} else {
			a.Failures = append(a.Failures, q.Score(a.Answer)...)
		}
		a.Passed = len(a.Failures) == 0
		if a.Passed {
			passed++
		}
		r.Questions = append(r.Questions, a)
	}
	r.Score = float64(passed) / float64(len(t.Set.Questions))
	if t.MeshCheck == nil {
		r.MeshError = "no mesh check"
		return
	}
	if err := t.MeshCheck(ctx); err != nil {
		r.MeshError = err.Error()
		return
	}
	r.Mesh = true
}

// MeshServices are the services the workload's cross-service edges lead
// to (workload/scenarios/microservices/expect.json: the gateway calls
// orders and inventory, orders calls inventory and billing). The task
// prompt names the projects after them.
var MeshServices = []string{"orders", "inventory", "billing"}

// MeshCommand prints the controller's edges; the guide installs
// iormeshctl as ~/bin/iormeshctl (chapter 3).
const MeshCommand = `PATH="$HOME/bin:$HOME/.local/bin:$PATH" iormeshctl show edges`

// ShellMeshCheck is the mesh check through the harness's own shell on the
// controller: `iormeshctl show edges` must hold a fused edge to each of
// MeshServices (the TO column of guide chapter 7).
func ShellMeshCheck(s *Shell, controller string) func(context.Context) error {
	return func(ctx context.Context) error {
		out, code, err := s.Run(ctx, Call{Host: controller, Command: MeshCommand, By: ByHarness})
		if err != nil {
			return err
		}
		if code != 0 {
			return fmt.Errorf("show edges exited %d: %s", code, strings.TrimSpace(out))
		}
		return CheckEdges(out, MeshServices)
	}
}

// CheckEdges checks `show edges` output (FROM TO STATE …) for a fused
// edge to each service. The table's columns are cut at the header's
// offsets, not split on whitespace: a FROM can hold spaces (a client
// with no port is named by its pattern, "app:gateway/*… --role gateway
// *"), which the first real trial showed (task nm4). TO names a
// listening service and STATE is one word.
func CheckEdges(out string, services []string) error {
	fused := map[string]bool{}
	to, state := -1, -1
	for _, l := range strings.Split(out, "\n") {
		if to < 0 {
			if strings.HasPrefix(l, "FROM") {
				to, state = headerColumn(l, "TO"), headerColumn(l, "STATE")
			}
			continue
		}
		if state < 0 || to >= state || len(l) <= state {
			continue
		}
		f := strings.Fields(l[state:])
		if len(f) > 0 && f[0] == "fused" {
			fused[strings.TrimSpace(l[to:state])] = true
		}
	}
	var missing []string
	for _, s := range services {
		if !fused[s] {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("no fused edge to %s in show edges", strings.Join(missing, ", "))
	}
	return nil
}

// headerColumn is the offset of the column name in a table header, or -1.
func headerColumn(header, name string) int {
	for i := 0; i+len(name) <= len(header); i++ {
		if header[i:i+len(name)] == name && (i == 0 || header[i-1] == ' ') &&
			(i+len(name) == len(header) || header[i+len(name)] == ' ') {
			return i
		}
	}
	return -1
}

// ResultName is the result's file name: date, model (and its effort, see
// modelLabel) and mode, the model as eval.FileLabel makes it a plain name
// part (M5's naming per model).
func ResultName(r Result) string {
	return r.Date + "-" + modelLabel(r.Model, r.Effort) + "-" + r.Mode + ".json"
}

// modelLabel is the model as a name part, followed by its effort unless
// that is the default: a run at medium keeps the name runs had before
// the effort was an option, and one model at two efforts gets two names.
func modelLabel(model, effort string) string {
	if effort == "" || effort == DefaultClaudeEffort {
		return eval.FileLabel(model)
	}
	return eval.FileLabel(model) + "-" + effort
}

// WriteResult writes the result as <dir>/ResultName, or, when a run of
// the same day, model and mode already wrote that, as <name>-2.json,
// -3, …: a rerun never overwrites an earlier result (#37 keeps every
// failed cell's record).
func WriteResult(dir string, r Result) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	base := strings.TrimSuffix(ResultName(r), ".json")
	for n := 1; ; n++ {
		name := base + ".json"
		if n > 1 {
			name = fmt.Sprintf("%s-%d.json", base, n)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, err = f.Write(append(data, '\n'))
		return path, errors.Join(err, f.Close())
	}
}

// PrintResult prints one line per question, the checks and the verdict.
func PrintResult(w io.Writer, r Result) {
	for _, q := range r.Questions {
		mark := "PASS"
		if !q.Passed {
			mark = "FAIL"
		}
		_, _ = fmt.Fprintf(w, "%s %-22s %v\n", mark, q.ID, q.Failures)
	}
	verdict := "FAILED"
	if r.Passed {
		verdict = "PASSED"
	}
	_, _ = fmt.Fprintf(w, "%s: %s %s, %d/%d turns, %.1f min, mesh %v %s, score %.2f (threshold %.2f) %s\n",
		verdict, r.Model, r.Mode, r.Turns, r.MaxTurns, r.Minutes, r.Mesh, r.MeshError, r.Score,
		r.Threshold, r.Failure)
}

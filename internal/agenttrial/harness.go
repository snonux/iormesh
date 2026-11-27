package agenttrial

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"iormesh/internal/eval"
)

// The trial's inputs in Options.Dir (decision #37: "the fixed task
// prompt and question set (eval/agent-trial/)") and its outputs: results
// are kept, runs (transcripts, command logs, the docs copy) are
// gitignored.
const (
	TaskFile      = "task.md"
	QuestionsFile = "questions.yaml"
	ScenarioFile  = "scenario.sh"
	ResultsDir    = "results"
	RunsDir       = "runs"
	// libraryBytes caps one READ (eval's SourceTree cap): the longest
	// guide chapter fits.
	libraryBytes = 64 << 10
	// releaseWait bounds the wait for the release tunnel.
	releaseWait = 30 * time.Second
)

// runFiles are one run's directory and open logs.
type runFiles struct {
	dir                   string
	transcript, commands  *os.File
	stderr                *os.File
	library, agentWorkdir string
}

// Main runs one trial: copy the docs, prepare the VMs, serve the release,
// play the run, write the result and print it. It fails when the run did
// not pass.
func Main(ctx context.Context, o Options, stdout io.Writer) error {
	set, err := eval.LoadSet(filepath.Join(o.Dir, QuestionsFile))
	if err != nil {
		return err
	}
	task, err := os.ReadFile(filepath.Join(o.Dir, TaskFile))
	if err != nil {
		return err
	}
	rf, err := openRun(o)
	if err != nil {
		return err
	}
	defer rf.close()
	shell := &Shell{SSH: o.SSH, Options: sshOptions(o), Hosts: []string{o.Controller, o.Traced},
		Log: rf.commands}
	rel := &ReleaseServer{Dir: o.ReleaseDir, Shell: shell, Controller: o.Controller, RemotePort: o.ReleasePort}
	defer rel.Stop()
	if err := prepare(ctx, o, shell, rel); err != nil {
		return fmt.Errorf("prepare the VMs: %w", err)
	}
	t, err := newTrial(o, rf, shell, set, string(task), rel.URL())
	if err != nil {
		return err
	}
	return report(o, rf, t.Run(ctx), stdout)
}

// openRun makes runs/<time>-<model>-<mode>/ with the docs copy and the
// logs, and the agent's empty working directory outside the checkout (so
// no CLI can find the repository's own CLAUDE.md or AGENTS.md above it).
func openRun(o Options) (*runFiles, error) {
	name := time.Now().UTC().Format("20060102T150405Z") + "-" + modelLabel(o.Model, o.Effort) + "-" + o.Mode
	rf := &runFiles{dir: filepath.Join(o.Dir, RunsDir, name)}
	rf.library = filepath.Join(rf.dir, "library")
	if err := os.MkdirAll(rf.library, 0o755); err != nil {
		return nil, err
	}
	if err := CopyLibrary(o.Repo, rf.library); err != nil {
		return nil, err
	}
	var err error
	if rf.agentWorkdir, err = os.MkdirTemp("", "iormesh-trial-agent-"); err != nil {
		return nil, err
	}
	for _, f := range []struct {
		p    **os.File
		name string
	}{{&rf.transcript, "transcript.jsonl"}, {&rf.commands, "commands.jsonl"}, {&rf.stderr, "agent-stderr.log"}} {
		if *f.p, err = os.Create(filepath.Join(rf.dir, f.name)); err != nil {
			rf.close()
			return nil, err
		}
	}
	return rf, nil
}

// close closes the logs and removes the agent's working directory: pi's
// session files live there. claude keeps its session under the
// operator's ~/.claude/projects/ (named after this directory); it stays,
// because claude finds its credentials under the same HOME, which the
// trial therefore leaves alone. The transcript is the record either way.
func (rf *runFiles) close() {
	for _, f := range []*os.File{rf.transcript, rf.commands, rf.stderr} {
		if f != nil {
			_ = f.Close()
		}
	}
	if rf.agentWorkdir != "" {
		_ = os.RemoveAll(rf.agentWorkdir)
	}
}

// sshOptions are the ssh options every call shares: the config file when
// one is named.
func sshOptions(o Options) []string {
	if o.SSHConfig == "" {
		return nil
	}
	return []string{"-F", o.SSHConfig}
}

// prepare prepares the VMs, then serves the release and proves the
// controller reaches it.
func prepare(ctx context.Context, o Options, shell *Shell, rel *ReleaseServer) error {
	p := Prep{Shell: shell, Controller: o.Controller, Traced: o.Traced, Workload: o.Workload,
		Scenario: filepath.Join(o.Dir, ScenarioFile), Password: NewPassword()}
	if err := p.Run(ctx); err != nil {
		return err
	}
	if err := rel.Start(ctx); err != nil {
		return err
	}
	deadline := time.Now().Add(releaseWait)
	for {
		err := p.must(ctx, Call{Host: o.Controller, Command: rel.CheckReleaseCommand()})
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(2 * time.Second)
	}
}

// newTrial assembles the trial: the agent CLI, the library and the first
// message.
func newTrial(o Options, rf *runFiles, shell *Shell, set eval.Set, task, url string) (*Trial, error) {
	lib := eval.SourceTree{Root: rf.library, MaxBytes: libraryBytes}
	files, err := lib.List()
	if err != nil {
		return nil, err
	}
	skill, err := lib.Source(SkillFile)
	if err != nil || strings.HasPrefix(skill, "error: ") {
		return nil, fmt.Errorf("the skill %s: %v %s", SkillFile, err, skill)
	}
	vars := map[string]string{"controller": o.Controller, "traced": o.Traced, "release_url": url}
	var agent Agent = &CLI{Kind: o.Agent, Model: o.Model, Effort: o.Effort, SystemPrompt: SystemPrompt,
		Dir: rf.agentWorkdir, Session: NewSessionID(), Stderr: rf.stderr}
	if o.agent != nil {
		agent = o.agent // the tests' stand-in
	}
	return &Trial{Agent: agent, Shell: shell, Library: lib, Set: set, Mode: o.Mode, MaxTurns: o.MaxTurns,
		Timeout: o.Timeout, First: FirstMessage(task, skill, files, set, vars), Transcript: rf.transcript,
		MeshCheck: ShellMeshCheck(shell, o.Controller)}, nil
}

// report labels the result, writes it under results/ and prints it.
func report(o Options, rf *runFiles, r Result, stdout io.Writer) error {
	r.Agent, r.Model, r.Effort, r.Date, r.Commit = o.Agent, o.Model, o.Effort, o.Date, o.Commit
	r.Transcript = filepath.Join(rf.dir, "transcript.jsonl")
	r.Commands = filepath.Join(rf.dir, "commands.jsonl")
	path, err := WriteResult(filepath.Join(o.Dir, ResultsDir), r)
	if err != nil {
		return err
	}
	PrintResult(stdout, r)
	_, _ = fmt.Fprintf(stdout, "result %s, transcript %s\n", path, r.Transcript)
	if !r.Passed {
		return fmt.Errorf("agent trial %s %s: failed", r.Model, r.Mode)
	}
	return nil
}

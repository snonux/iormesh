//go:build mage

// The LLM introspection eval (PLAN.md M5, internal/eval): EvalTrace
// records the microservices scenario's trace into a store, Eval puts the
// question set to a model over it. Both are opt-in and neither is part
// of Test: the eval sends the trace's rollup and query slices to the
// model command the operator names, and never picks one itself.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"

	"iormesh/internal/gatecmd"
)

const (
	// evalStoreDefault is where EvalTrace leaves its store and Eval looks
	// for it unless IORMESH_EVAL_STORE says otherwise (git-ignored).
	evalStoreDefault = "eval/store"
	evalModelCmdEnv  = "IORMESH_EVAL_MODEL_CMD"
	evalModelNameEnv = "IORMESH_EVAL_MODEL_NAME"
	evalStoreEnv     = "IORMESH_EVAL_STORE"
	// evalExportEnv tells TestMicroservices to export its judged run's
	// segments (integrationtests/microservices_test.go).
	evalExportEnv = "IORMESH_EVAL_EXPORT"
)

// Eval runs the question set (eval/questions/microservices.yaml) over
// the store in IORMESH_EVAL_STORE (default eval/store, made by
// evalTrace) with the model command in IORMESH_EVAL_MODEL_CMD (prompt on
// stdin, reply on stdout); IORMESH_EVAL_MODEL_NAME labels the results in
// eval/results/. Without a model command it refuses to run.
func Eval() error {
	model := os.Getenv(evalModelCmdEnv)
	if model == "" {
		return errors.New(evalModelCmdEnv + " is not set: the eval sends the trace's rollup and " +
			"slices to that command, so name one explicitly (a local model keeps them on this machine)")
	}
	store := evalStore()
	if _, err := os.Stat(store); err != nil {
		return fmt.Errorf("no eval store at %s (mage evalTrace records one): %w", store, err)
	}
	if err := sh.RunWithV(map[string]string{"CGO_ENABLED": "0"},
		"go", "build", "-o", ctlBinaryName, "./cmd/iormeshctl"); err != nil {
		return err
	}
	name := os.Getenv(evalModelNameEnv)
	if name == "" {
		name = "unnamed"
	}
	return sh.RunWithV(map[string]string{"CGO_ENABLED": "0"}, "go", "run", "./cmd/iormesheval",
		"-store", store, "-model-cmd", model, "-model-name", name, "-iormeshctl", "./"+ctlBinaryName)
}

// EvalTrace records the eval's trace (requires root, sudo -n): it runs
// TestMicroservices once, which — its golden passed — exports the run's
// segments into IORMESH_EVAL_STORE (default eval/store) as a collected
// store owned by the calling user. An existing store is replaced — only
// one that holds nothing but a store's layout (isEvalStore).
func EvalTrace() error {
	store, err := filepath.Abs(evalStore())
	if err != nil {
		return err
	}
	if err := isEvalStore(store); err != nil {
		return err // before any build: a wrong path fails at once
	}
	mg.SerialDeps(Build, WorkloadBuild, AgentBuild)
	if err := os.RemoveAll(store); err != nil {
		return err
	}
	if err := os.MkdirAll(store, 0o700); err != nil {
		return err
	}
	if err := sh.RunWithV(goEnv(), "go", "test", "-c", "./integrationtests/",
		"-o", gatecmd.IntegrationTestBinary); err != nil {
		return err
	}
	if err := os.Setenv(evalExportEnv, store); err != nil {
		return err
	}
	if err := runRootTestBinary(gatecmd.IntegrationTestBinary, "integrationtests", os.Stdout,
		"-test.run", "^TestMicroservices$", "-test.count", "1", "-test.v"); err != nil {
		return err
	}
	entries, err := filepath.Glob(filepath.Join(store, "*", "*", "*.parquet"))
	if err != nil || len(entries) == 0 {
		return fmt.Errorf("TestMicroservices exported no segments to %s (skipped as unexercised?)", store)
	}
	fmt.Printf("eval store %s: %d segment files\n", store, len(entries))
	return nil
}

// isEvalStore refuses to let EvalTrace replace a directory that is not
// a store a previous EvalTrace left: IORMESH_EVAL_STORE could name a home
// directory by mistake. A missing directory is fine; otherwise every
// entry must be <project>/<host>/<segment>.parquet — directories two
// levels deep, regular parquet files on the third, nothing else.
func isEvalStore(store string) error {
	if _, err := os.Lstat(store); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(store, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(store, p)
		if err != nil || rel == "." {
			return err
		}
		depth := len(strings.Split(rel, string(filepath.Separator)))
		ok := d.IsDir() && depth <= 2 ||
			d.Type().IsRegular() && depth == 3 && strings.HasSuffix(p, ".parquet")
		if !ok {
			return fmt.Errorf("%s holds %s, which is no part of an eval store: not replacing it", store, rel)
		}
		return nil
	})
}

// evalStore is IORMESH_EVAL_STORE or the default.
func evalStore() string {
	if s := os.Getenv(evalStoreEnv); s != "" {
		return s
	}
	return evalStoreDefault
}

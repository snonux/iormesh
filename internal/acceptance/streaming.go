package acceptance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// phaseStreaming runs `collect` Rounds times during the live sessions,
// each after new work and a segment rotation, and checks the ready/
// contract on every round (M4-2) and that collect touches only the store
// (M4-3).
func (r *Runner) phaseStreaming(ctx context.Context) {
	r.phase("streaming collect")
	moved, collectOnly := 0, error(nil)
	for i := 1; i <= r.cfg.Rounds; i++ {
		rr, err := r.streamRound(ctx, i)
		collectOnly = firstErr(collectOnly, rr.onlyStore)
		if err != nil {
			r.res.set(cStream, Fail, "round %d: %v", i, err)
			r.res.setErr(cCollect, collectOnly, collectOnlyDetail)
			return
		}
		moved += rr.moved
	}
	if moved == 0 {
		r.res.set(cStream, Fail, "no segment was published in %d rounds", r.cfg.Rounds)
	} else {
		r.res.set(cStream, Pass, "%d rounds, %d published files collected and deleted on the servers; "+
			"only ready/ and the active segment between collects", r.cfg.Rounds, moved)
	}
	r.res.setErr(cCollect, collectOnly, collectOnlyDetail)
}

const collectOnlyDetail = "registry, service db, builds and deploy record unchanged by every collect"

// roundResult is one streaming round's outcome besides its error (a
// ready/-contract violation or a failed step): the files moved, and
// whether collect changed anything outside the store.
type roundResult struct {
	moved     int
	onlyStore error
}

// streamRound makes new work, waits out a rotation, and collects once.
func (r *Runner) streamRound(ctx context.Context, i int) (roundResult, error) {
	dir, err := r.scenarioDir(ctx, r.cfg.Host1, fmt.Sprintf("file-basic/r%d", i))
	if err != nil {
		return roundResult{}, err
	}
	if err := r.java(ctx, r.cfg.Host1, "file-basic", "solo", dir); err != nil {
		return roundResult{}, err
	}
	r.logf("  waiting %s for the segment rotation", r.cfg.Rotation)
	if err := r.sleep(ctx, r.cfg.Rotation); err != nil {
		return roundResult{}, err
	}
	before, err := r.listReady(ctx)
	if err != nil {
		return roundResult{}, err
	}
	snapBefore, err := snapshot(r.cfg.WorkDir, r.storeRel(), ".iormesh")
	if err != nil {
		return roundResult{}, err
	}
	if _, err := r.ctlRun(ctx, "collect"); err != nil {
		return roundResult{}, err
	}
	rr := roundResult{onlyStore: r.compareSnapshot(snapBefore)}
	rr.moved, err = r.checkMoved(ctx, before)
	return rr, err
}

// storeRel is the trace store relative to the work dir.
func (r *Runner) storeRel() string {
	rel, _ := filepath.Rel(r.cfg.WorkDir, r.storeDir())
	return rel
}

// compareSnapshot fails when a file outside the store changed.
func (r *Runner) compareSnapshot(before map[string]string) error {
	after, err := snapshot(r.cfg.WorkDir, r.storeRel(), ".iormesh")
	if err != nil {
		return err
	}
	if d := diffSnapshots(before, after); len(d) > 0 {
		return fmt.Errorf("collect changed files outside the store: %s", strings.Join(d, ", "))
	}
	return nil
}

// readyKey names one project on one host.
type readyKey struct{ host, project string }

// listReady lists every project's ready/ on both hosts.
func (r *Runner) listReady(ctx context.Context) (map[readyKey][]string, error) {
	out := map[readyKey][]string{}
	for _, p := range r.cfg.projects() {
		ls, err := r.remote(ctx, p.host, "ls", "-1A", path.Join(traceRoot, p.name, "ready"))
		if err != nil {
			return nil, err
		}
		out[readyKey{p.host, p.name}] = lines(ls)
	}
	return out, nil
}

// checkMoved checks the ready/ contract after a collect: every file
// listed before is gone from the server and present in the store, and
// the project directory holds nothing but ready/, the state, the lock and
// the one active segment.
func (r *Runner) checkMoved(ctx context.Context, before map[readyKey][]string) (int, error) {
	after, err := r.listReady(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for k, names := range before {
		local := filepath.Join(r.storeDir(), k.project, k.host)
		for _, name := range names {
			if contains(after[k], name) {
				return n, fmt.Errorf("%s:%s/ready/%s still on the server after collect", k.host, k.project, name)
			}
			if _, err := os.Stat(filepath.Join(local, name)); err != nil {
				return n, fmt.Errorf("%s not in the store: %w", name, err)
			}
			n++
		}
		if err := r.checkProjectDir(ctx, k); err != nil {
			return n, err
		}
	}
	return n, nil
}

// checkProjectDir allows only what a live session keeps at the top of its
// project directory: ready/, state.json, .lock and .active-* (the one
// segment being written).
func (r *Runner) checkProjectDir(ctx context.Context, k readyKey) error {
	ls, err := r.remote(ctx, k.host, "ls", "-1A", path.Join(traceRoot, k.project))
	if err != nil {
		return err
	}
	var extra []string
	for _, f := range lines(ls) {
		if f != "ready" && f != "state.json" && f != ".lock" && !strings.HasPrefix(f, ".active-") {
			extra = append(extra, f)
		}
	}
	if len(extra) > 0 {
		return errors.New(k.host + ":" + k.project + " holds unexpected files: " + strings.Join(extra, ", "))
	}
	return nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

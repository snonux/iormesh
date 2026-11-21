package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// phaseDeploy registers every project and deploys both hosts — the
// operator's two commands — then checks each session runs. It reports
// whether the run can go on.
func (r *Runner) phaseDeploy(ctx context.Context) bool {
	r.phase("add and deploy")
	for _, p := range r.cfg.projects() {
		if err := r.add(ctx, p); err != nil {
			r.res.set(cDeploy, Fail, "%v", err)
			return false
		}
	}
	if _, err := r.ctlRun(ctx, "deploy", r.cfg.Host1, r.cfg.Host2); err != nil {
		r.res.set(cDeploy, Fail, "%v", err)
		return false
	}
	for _, p := range r.cfg.projects() {
		if err := r.expectState(ctx, p.host, p.name, "running", ""); err != nil {
			r.res.set(cDeploy, Fail, "%v", err)
			return false
		}
	}
	r.res.set(cDeploy, Pass, "%d projects running on %s and %s", len(r.cfg.projects()),
		r.cfg.Host1, r.cfg.Host2)
	return true
}

// add registers one project; extra are tracer flags after the host. An
// agent project also gets its spool and the installed agent jar, which
// makes deploy ship the jar to its host (agent.go).
func (r *Runner) add(ctx context.Context, p project, extra ...string) error {
	args := append([]string{"add", "-project", p.name, "-pattern", p.pattern, p.host}, tracerFlags...)
	if p.agent {
		args = append(args, agentFlags(p.name)...)
	}
	if _, err := r.ctlRun(ctx, append(args, extra...)...); err != nil {
		return err
	}
	r.added[p.host] = true
	return nil
}

// sessionStatus is the part of `iormesh status -json` the run reads.
type sessionStatus struct {
	Project string `json:"project"`
	State   string `json:"state"`
	Session *struct {
		EndReason string `json:"end_reason"`
	} `json:"session"`
	ReadySegments int `json:"ready_segments"`
}

// status reads one project's session state through the sudoers rule
// (status is one of the subcommands it allows; read-only).
func (r *Runner) status(ctx context.Context, host, proj string) (sessionStatus, error) {
	out, err := r.remote(ctx, host, "sudo", "-n", installedBin, "status", "-json", "-project", proj)
	if err != nil {
		return sessionStatus{}, err
	}
	var reps []sessionStatus
	if err := json.Unmarshal([]byte(out), &reps); err != nil || len(reps) != 1 {
		return sessionStatus{}, fmt.Errorf("%s: status -json of %s: %q (%v)", host, proj, out, err)
	}
	return reps[0], nil
}

// expectState fails unless the project's state (and, when given, its end
// reason) is the wanted one.
func (r *Runner) expectState(ctx context.Context, host, proj, state, reason string) error {
	st, err := r.status(ctx, host, proj)
	if err != nil {
		return err
	}
	got := ""
	if st.Session != nil {
		got = st.Session.EndReason
	}
	if st.State != state || (reason != "" && got != reason) {
		return fmt.Errorf("%s project %s: state %q end %q, want %q %s", host, proj, st.State, got,
			state, strings.TrimSpace(reason))
	}
	return nil
}

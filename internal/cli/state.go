package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// StateFile is a project's session state, <trace-dir>/<project>/state.json
// (PLAN.md M4, decision #19): what `iormesh status` reports and what
// `start` waits on. The run itself writes it, not start, so it describes
// the tracer as it is — attached, or ended and why — for a foreground
// run as much as for a unit's.
const StateFile = "state.json"

// The states a run records. A run that crashed or was killed leaves
// running behind; status tells that apart by the pid (stateDead).
const (
	stateRunning = "running"
	stateEnded   = "ended"
	// stateDead is never written: status reports it for a running state
	// whose process is gone.
	stateDead = "dead"
)

// sessionState is state.json. Times are UTC.
type sessionState struct {
	Project string `json:"project"`
	Session string `json:"session"`
	FQDN    string `json:"fqdn"`
	// PID and PIDStartNS name the tracer process and its incarnation (its
	// start on the boot clock), so a reused pid is not taken for it.
	PID        int    `json:"pid"`
	PIDStartNS uint64 `json:"pid_start_ns"`
	// InvocationID is systemd's $INVOCATION_ID, empty outside a unit:
	// start matches it against the unit's to know that this state is its
	// run's, not a previous one's.
	InvocationID string    `json:"invocation_id,omitempty"`
	State        string    `json:"state"`
	Targets      string    `json:"targets"`
	StartedAt    time.Time `json:"started_at"`
	EndsBy       time.Time `json:"ends_by"`
	EndedAt      time.Time `json:"ended_at,omitzero"`
	EndReason    string    `json:"end_reason,omitempty"`
}

// markRunning writes state.json once the probes are attached (beside
// announceReady): the session is tracing from here on. Nothing without
// -trace-dir. The project directory was checked and locked by
// segment.New, so only this run writes there.
func (o *segOutput) markRunning(cfg runConfig, targets string, now time.Time) error {
	if o == nil {
		return nil
	}
	pid := os.Getpid()
	return writeState(o.dir, sessionState{Project: cfg.project, Session: o.session, FQDN: o.fqdn,
		PID: pid, PIDStartNS: processStart(uint32(pid)), InvocationID: os.Getenv("INVOCATION_ID"),
		State: stateRunning, Targets: targets, StartedAt: now.UTC(), EndsBy: now.Add(cfg.duration).UTC()})
}

// markEnded records the end in state.json, if markRunning wrote it. It is
// called before the writer closes: after Close the lock is gone and the
// next run of the project may already be writing its own state.
func (o *segOutput) markEnded(reason string, now time.Time) error {
	if o == nil {
		return nil
	}
	st, err := readState(o.dir)
	if errors.Is(err, os.ErrNotExist) || (err == nil && st.Session != o.session) {
		return nil // the run ended before it attached: nothing to update
	}
	if err != nil {
		return err
	}
	st.State, st.EndReason, st.EndedAt = stateEnded, reason, now.UTC()
	return writeState(o.dir, st)
}

// writeState replaces dir/state.json atomically (a temporary file, fsync,
// rename), so status never reads half a file. 0644: status runs under
// sudo, but the deploying user may read it too, like the segments.
func writeState(dir string, st sessionState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	tmp := filepath.Join(dir, "."+StateFile+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	_, werr := f.Write(append(b, '\n'))
	if err := errors.Join(werr, f.Sync(), f.Close()); err != nil {
		return fmt.Errorf("state: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, StateFile)); err != nil {
		return fmt.Errorf("state: %w", err)
	}
	return nil
}

// readState reads dir/state.json. A missing file wraps os.ErrNotExist; a
// file that is not a state (corrupt, or for another project's name) is an
// error of its own, never a silently empty state.
func readState(dir string) (sessionState, error) {
	b, err := os.ReadFile(filepath.Join(dir, StateFile))
	if err != nil {
		return sessionState{}, fmt.Errorf("state: %w", err)
	}
	var st sessionState
	if err := json.Unmarshal(b, &st); err != nil {
		return sessionState{}, fmt.Errorf("state: %s: corrupt: %w", filepath.Join(dir, StateFile), err)
	}
	if st.State != stateRunning && st.State != stateEnded || st.Session == "" {
		return sessionState{}, fmt.Errorf("state: %s: corrupt: state %q, session %q",
			filepath.Join(dir, StateFile), st.State, st.Session)
	}
	return st, nil
}

// effective is the state status reports: a running state whose process
// is gone (crash, SIGKILL, power loss) is dead — its active minute was
// never published.
func (st sessionState) effective() string {
	if st.State == stateRunning && !processAlive(uint32(st.PID), st.PIDStartNS) {
		return stateDead
	}
	return st.State
}

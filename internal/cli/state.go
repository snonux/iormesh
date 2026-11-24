package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"iormesh/internal/version"
)

// StateFile is a project's session state, <trace-dir>/<project>/state.json
// (docs/milestones.md M4, decision #19): what `iormesh status` reports and
// what `start` waits on. The run itself writes it, not start, so it describes
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
	// Version and Argv are the build and the command line of the run
	// (task ph4): upgrade replaces the binary, not a running session, so
	// only the run itself can say which tracer and flags it has.
	// iormeshctl status shows the version, to make a version skew
	// visible; Argv is there for an operator reading the file. Both
	// are omitted by tracers from before them; a reader treats the empty
	// values as unknown, never as an error.
	Version string   `json:"version,omitempty"`
	Argv    []string `json:"argv,omitempty"`
	// OperatorStop is true once `iormesh stop` ended (or is ending) the
	// session (task zh4): the end reason alone cannot say so, since a
	// reboot's or anyone's SIGTERM ends a run as "stop" too, and
	// iormeshctl ensure restarts every end but the operator's. The run
	// writes false from its start, so a reader tells "not the operator"
	// (false) from a tracer that predates the field (absent: nil).
	OperatorStop *bool `json:"operator_stop,omitempty"`
}

// markRunning writes state.json once the probes are attached (beside
// announceReady): the session is tracing from here on. Nothing without
// -trace-dir. The project directory was checked and locked by
// segment.New, so only this run writes there. It records this binary's
// version and os.Args, the run's own argv (for a unit, the `run` argv
// start handed systemd-run). OperatorStop starts out false: only a later
// `iormesh stop` sets it (markOperatorStop).
func (o *segOutput) markRunning(cfg runConfig, targets string, now time.Time) error {
	if o == nil {
		return nil
	}
	pid := os.Getpid()
	return writeState(o.dir, sessionState{Project: cfg.project, Session: o.session, FQDN: o.fqdn,
		PID: pid, PIDStartNS: processStart(uint32(pid)), InvocationID: os.Getenv("INVOCATION_ID"),
		State: stateRunning, Targets: targets, StartedAt: now.UTC(), EndsBy: now.Add(cfg.duration).UTC(),
		Version: version.Stamped(), Argv: slices.Clone(os.Args), OperatorStop: new(false)})
}

// markOperatorStop sets OperatorStop in dir's state.json, if there is
// one (task zh4). It is an unlocked read-modify-write, so stop calls it
// only once the unit is dead and the run can no longer write the file:
// earlier, a run's start or end record landing between the read and the
// rename would be rolled back. Nothing to mark is not an error: a run
// that never attached has no state to carry it.
func markOperatorStop(dir string) error {
	st, err := readState(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.OperatorStop != nil && *st.OperatorStop {
		return nil
	}
	st.OperatorStop = new(true)
	return writeState(dir, st)
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
// sudo, but the deploying user may read it too, like the segments. The
// temporary name carries the writer's pid: the run and `iormesh stop`
// (markOperatorStop) never share one, even should a dying run's write
// overlap the stop's. A failed write or rename removes the
// temporary again.
func writeState(dir string, st sessionState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.%d.tmp", StateFile, os.Getpid()))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	_, werr := f.Write(append(b, '\n'))
	if err := errors.Join(werr, f.Sync(), f.Close()); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("state: %w", err)
	}
	if beforeStateRename != nil {
		beforeStateRename(dir)
	}
	if err := os.Rename(tmp, filepath.Join(dir, StateFile)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("state: %w", err)
	}
	return nil
}

// beforeStateRename, when set, runs in writeState between the temporary's
// sync and its rename over state.json: a test's stand-in for another
// writer (the run) landing its own write right then. Nil in production.
var beforeStateRename func(dir string)

// readState reads dir/state.json. A missing file wraps os.ErrNotExist; a
// file that is not a state (corrupt, or for another project's name) is an
// error of its own, never a silently empty state.
func readState(dir string) (sessionState, error) {
	b, err := readStateFile(filepath.Join(dir, StateFile))
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

// maxStateSize bounds a state file read: a real one is under 1 KiB.
const maxStateSize = 64 << 10

// readStateFile reads path as root would safely read a file under a
// -trace-dir the caller chose (status, stop): not through a symlink, only
// a regular file, at most maxStateSize — so status can neither be pointed
// at another file nor stalled on a FIFO or a huge one.
func readStateFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxStateSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxStateSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxStateSize)
	}
	return b, nil
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

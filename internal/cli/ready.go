package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"iormesh/internal/k8s"
	"iormesh/internal/project"
	"iormesh/internal/segment"
)

// readyCommand is `iormesh ready -project NAME [-state | -remove --
// FILE...]`, the tracer half of the k8s trace fetch (decisions #12 and
// #18, internal/k8s): it writes the project's ready/ segments as a tar
// to stdout, or with -remove deletes the named ones once the controller
// has them, or with -state prints whether the project's session has
// attached (readyState: what deploy waits for, task ef4), or with
// -status prints its status report (readyStatus: what iormeshctl status
// reads, task ph4). It takes a
// project, never a path — the trace dir is the fixed k8s.TraceRoot,
// inside the pod's emptyDir — so an exec into the tracer can read or
// delete nothing but published segments. No tar, rsync or shell is
// needed in the image.
func readyCommand(args []string, stdout, stderr io.Writer) error {
	return readyIn(k8s.TraceRoot, args, stdout, stderr)
}

// readyIn is readyCommand under the trace root root, which only a test
// changes.
func readyIn(root string, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("ready", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("project", "", "project label")
	remove := fs.Bool("remove", false, "delete the named segments instead of streaming")
	state := fs.Bool(strings.TrimPrefix(k8s.ReadyStateFlag, "-"), false,
		"print the session's attach state instead of streaming")
	status := fs.Bool(strings.TrimPrefix(k8s.ReadyStatusFlag, "-"), false,
		"print the session's status report as JSON instead of streaming")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if !project.Valid(*name) {
		return fmt.Errorf("%w: -project %q: %s", errUsage, *name, project.Rule)
	}
	if *status {
		if *state || *remove || fs.NArg() != 0 {
			return fmt.Errorf("%w: -status takes no -state, no -remove and no arguments", errUsage)
		}
		return readyStatus(stdout, root, *name)
	}
	if *state {
		if *remove || fs.NArg() != 0 {
			return fmt.Errorf("%w: -state takes no -remove and no arguments", errUsage)
		}
		return readyState(stdout, filepath.Join(root, *name))
	}
	dir := filepath.Join(root, *name, segment.ReadyDir)
	if *remove {
		return k8s.RemoveReady(dir, fs.Args())
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: unexpected argument %q", errUsage, fs.Arg(0))
	}
	// No project dir at all is not "nothing published yet" (WriteReady's
	// empty stream for a missing ready/): no session of this project ever
	// started here, which a project mismatch between the registry and
	// the pod looks like. It fails, so collect says so instead of
	// reporting an empty fetch (task af4 review N5).
	if fi, err := os.Stat(filepath.Join(root, *name)); err != nil || !fi.IsDir() {
		return fmt.Errorf("project %s has no trace dir under %s: no session of it ran in this container", *name, root)
	}
	return k8s.WriteReady(stdout, stderr, dir)
}

// readyState prints the attach state of the session whose project dir
// is dir, one line for k8s.Kubectl.WaitAttached: "pending" before the
// run wrote state.json (it does once every probe is attached), else
// "running <session>", "ended <reason>" or "dead" (a running state
// whose process is gone, status's rule). A corrupt state.json fails.
// The project dir is the one-per-pod emptyDir's, so a state.json there
// is this pod's session, never an earlier pod's — but a debug tracer's
// predecessor in the same pod (iormesh-N, task ah4) leaves its own
// there, so these three states end with "started=<RFC3339Nano>", the
// session's StartedAt (k8s.AttachStartedKey, task eh4): the wait
// compares it with the new tracer container's start to tell the
// predecessor's state from the new run's. An ended session without a
// reason prints none rather than an empty word.
func readyState(stdout io.Writer, dir string) error {
	st, err := readState(dir)
	if errors.Is(err, os.ErrNotExist) {
		_, err = fmt.Fprintln(stdout, k8s.AttachPending)
		return err
	}
	if err != nil {
		return err
	}
	words := []string{st.effective()}
	switch words[0] {
	case stateRunning:
		words = append(words, st.Session)
	case stateEnded:
		words = append(words, st.EndReason)
	}
	if !st.StartedAt.IsZero() {
		words = append(words, k8s.AttachStartedKey+st.StartedAt.UTC().Format(time.RFC3339Nano))
	}
	words = slices.DeleteFunc(words, func(w string) bool { return w == "" })
	_, err = fmt.Fprintln(stdout, strings.Join(words, " "))
	return err
}

// readyStatus prints the status report of the project label under root
// as one JSON object: `iormesh status -json`'s report (statusReport)
// without the unit, which a pod's tracer container does not have — the
// container is the session. No state.json yet is a report without a
// session (the run has not attached); a corrupt one, or one naming
// another project, fails rather than print a report the controller would
// take for a session.
func readyStatus(stdout io.Writer, root, label string) error {
	dir := filepath.Join(root, label)
	r := statusReport{Project: label, ReadySegments: countReady(filepath.Join(dir, segment.ReadyDir))}
	st, err := readState(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	case st.Project != label:
		return fmt.Errorf("%s names project %q", filepath.Join(dir, StateFile), st.Project)
	default:
		r.State, r.Session = st.effective(), &st
	}
	return json.NewEncoder(stdout).Encode(r)
}

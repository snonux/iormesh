package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"iormesh/internal/k8s"
	"iormesh/internal/project"
	"iormesh/internal/segment"
)

// readyCommand is `iormesh ready -project NAME [-state | -remove --
// FILE...]`, the tracer half of the k8s trace fetch (decisions #12 and
// #18, internal/k8s): it writes the project's ready/ segments as a tar
// to stdout, or with -remove deletes the named ones once the controller
// has them, or with -state prints whether the project's session has
// attached (readyState: what deploy waits for, task ef4). It takes a project, never a path — the trace dir is the fixed
// k8s.TraceRoot, inside the pod's emptyDir — so an exec into the tracer can
// read or delete nothing but published segments. No tar, rsync or shell
// is needed in the image.
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
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if !project.Valid(*name) {
		return fmt.Errorf("%w: -project %q: %s", errUsage, *name, project.Rule)
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
// is this pod's session, never an earlier pod's.
func readyState(stdout io.Writer, dir string) error {
	st, err := readState(dir)
	if errors.Is(err, os.ErrNotExist) {
		_, err = fmt.Fprintln(stdout, k8s.AttachPending)
		return err
	}
	if err != nil {
		return err
	}
	line := st.effective()
	switch line {
	case stateRunning:
		line += " " + st.Session
	case stateEnded:
		line += " " + st.EndReason
	}
	_, err = fmt.Fprintln(stdout, line)
	return err
}

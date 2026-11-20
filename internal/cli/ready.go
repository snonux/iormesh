package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"iormesh/internal/k8s"
	"iormesh/internal/project"
	"iormesh/internal/segment"
)

// readyCommand is `iormesh ready -project NAME [-remove -- FILE...]`, the
// tracer half of the k8s trace fetch (decisions #12 and #18,
// internal/k8s): it writes the project's ready/ segments as a tar to
// stdout, or with -remove deletes the named ones once the controller has
// them. It takes a project, never a path — the trace dir is the fixed
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
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if !project.Valid(*name) {
		return fmt.Errorf("%w: -project %q: %s", errUsage, *name, project.Rule)
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

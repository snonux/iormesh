package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"iormesh/internal/agentspool"
	"iormesh/internal/correlate"
	"iormesh/internal/schema"
)

// javaOutput is a -spool run's Java join (M3): the spool directory the
// agents' rings appear in, the joiner between the correlator and the
// events file, and the method and thread tables it fills. A nil
// *javaOutput is a run without -spool; its methods are no-ops then, as
// stackOutput's are.
type javaOutput struct {
	join    *correlate.JavaJoin
	dir     *agentspool.Dir
	methods *schema.TableWriter[schema.Method]
	threads *schema.TableWriter[schema.Thread]
	stderr  io.Writer
	skipped int
}

// openJava creates the spool directory and the two table files, or,
// without -spool, removes stale tables a previous run left in cfg.out:
// they would describe ids no event of this trace carries.
func openJava(cfg runConfig, stderr io.Writer) (*javaOutput, error) {
	if cfg.spool == "" {
		return nil, removeStale(cfg.out, MethodsFile, ThreadsFile)
	}
	if err := agentspool.MakeSpool(cfg.spool); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.out, 0o755); err != nil {
		return nil, err
	}
	mf, err := os.Create(filepath.Join(cfg.out, MethodsFile))
	if err != nil {
		return nil, err
	}
	tf, err := os.Create(filepath.Join(cfg.out, ThreadsFile))
	if err != nil {
		return nil, errors.Join(err, mf.Close())
	}
	return &javaOutput{dir: agentspool.NewDir(cfg.spool), stderr: stderr,
		methods: schema.NewTableWriter[schema.Method](mf),
		threads: schema.NewTableWriter[schema.Thread](tf)}, nil
}

// removeStale deletes the named files of dir that may be left over.
func removeStale(dir string, names ...string) error {
	for _, n := range names {
		if err := os.Remove(filepath.Join(dir, n)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stale %s: %w", n, err)
		}
	}
	return nil
}

// sink puts the joiner in front of next; without -spool it is next.
func (o *javaOutput) sink(next schema.Sink) schema.Sink {
	if o == nil {
		return next
	}
	o.join = correlate.NewJavaJoin(correlate.JavaJoinConfig{Next: next, Tables: o,
		Scan: o.scan})
	return o.join
}

// scan hands the joiner the rings that appeared, and reports each file
// the spool refused (a planted or foreign file) once on stderr.
func (o *javaOutput) scan() ([]correlate.AgentRing, error) {
	rings, skipped, err := o.dir.Scan()
	for _, s := range skipped {
		o.skipped++
		_, _ = fmt.Fprintf(o.stderr, "iormesh: skipped agent ring: %v\n", s)
	}
	out := make([]correlate.AgentRing, len(rings))
	for i, r := range rings {
		out[i] = r
	}
	return out, err
}

// Method and Thread make javaOutput the joiner's correlate.JavaTables.
func (o *javaOutput) Method(m schema.Method) error { return o.methods.Write(m) }

func (o *javaOutput) Thread(t schema.Thread) error { return o.threads.Write(t) }

// poll lets the joiner read the rings while the stream is idle.
func (o *javaOutput) poll() error {
	if o == nil || o.join == nil {
		return nil
	}
	return o.join.Poll()
}

// close closes the tables; the joiner itself is closed by the
// correlator, as its sink, before.
func (o *javaOutput) close() error {
	if o == nil {
		return nil
	}
	return errors.Join(o.methods.Close(), o.threads.Close())
}

// summary is the Java part of the run's summary line: rings read, events
// joined (and how many of them a virtual thread's), why the others of
// those JVMs stayed none, and what the agents lost.
func (o *javaOutput) summary() string {
	if o == nil || o.join == nil {
		return ""
	}
	s := o.join.Stats()
	return fmt.Sprintf(", java rings %d (skipped %d, corrupt %d), brackets %d, attributed %d "+
		"(carrier %d; none: unbracketed %d, other kind %d, ambiguous %d, no stack %d, "+
		"timed out %d), agent lost %d", s.Rings, o.skipped, s.Corrupt, s.Brackets,
		s.Attributed, s.Carrier, s.Unbracketed, s.OtherKind, s.Ambiguous, s.NoStack,
		s.TimedOut, s.Lost.Total())
}

package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"iormesh/internal/agentspool"
	"iormesh/internal/bpf"
	"iormesh/internal/correlate"
	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// javaOutput is a -spool run's Java join (M3): the spool directory the
// agents' rings appear in, the joiner between the correlator and the
// events file, and the method and thread tables it fills. A nil
// *javaOutput is a run without -spool; its methods are no-ops then, as
// stackOutput's are.
type javaOutput struct {
	join    *correlate.JavaJoin
	marker  javaMarker
	dir     *agentspool.Dir
	methods *schema.TableWriter[schema.Method]
	threads *schema.TableWriter[schema.Thread]
	stderr  io.Writer
	skipped int
}

// javaMarker probes a JVM's natives for the Java mark (task td4);
// *bpf.Tracer implements it.
type javaMarker interface {
	AttachJavaMarks(pid uint32, natives []bpf.JavaNative) (missing []string, err error)
}

// openJava creates the spool directory and the two table files, or,
// without -spool, removes stale tables a previous run left in cfg.out:
// they would describe ids no event of this trace carries. marker is the
// tracer, opened with Config.JavaMarks when cfg.spool is set.
func openJava(cfg runConfig, marker javaMarker, stderr io.Writer) (*javaOutput, error) {
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
	return &javaOutput{dir: agentspool.NewDir(cfg.spool), marker: marker, stderr: stderr,
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
		Scan: o.scan, AttachMarks: o.attachMarks, Warn: o.warn})
	return o.join
}

// attachMarks probes one JVM's natives through the tracer, in the
// joiner's terms.
func (o *javaOutput) attachMarks(pid uint32, natives []correlate.MarkedNative) ([]string, error) {
	out := make([]bpf.JavaNative, len(natives))
	for i, n := range natives {
		out[i] = bpf.JavaNative{Symbol: n.Symbol, Cookie: n.Cookie}
	}
	return o.marker.AttachJavaMarks(pid, out)
}

func (o *javaOutput) warn(err error) {
	_, _ = fmt.Fprintf(o.stderr, "iormesh: %v\n", err)
}

// mark hands the joiner a kernel Java mark (the session routes every
// *types.JavaMarkEvent here, never to the correlator).
func (o *javaOutput) mark(r *types.JavaMarkEvent) {
	if o == nil || o.join == nil {
		return
	}
	o.join.Mark(correlate.NativeMark{PID: r.Pid, TID: r.Tid, EnterBoot: r.Time,
		Cookie: r.Cookie, EntryMono: r.Entry})
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

// summary is the Java part of the run's summary line: rings read (and
// how many JVMs' natives could not be probed), events joined (and how
// many of them a virtual thread's), why the others of those JVMs stayed
// none, how many (joined or not) left by the timeout rather than a
// watermark, the http events (and how many found no connection), and
// what the agents lost.
func (o *javaOutput) summary() string {
	if o == nil || o.join == nil {
		return ""
	}
	s := o.join.Stats()
	return fmt.Sprintf(", java rings %d (skipped %d, corrupt %d, unmarked JVMs %d), brackets %d, "+
		"attributed %d (carrier %d; none: unmarked %d, other native %d, unbracketed %d, "+
		"other kind %d, ambiguous %d, no stack %d), released by timeout %d, http %d "+
		"(no connection %d), agent lost %d",
		s.Rings, o.skipped, s.Corrupt, s.MarkFailed, s.Brackets, s.Attributed, s.Carrier,
		s.Unmarked, s.OtherNative, s.Unbracketed, s.OtherKind, s.Ambiguous, s.NoStack,
		s.TimedOut, s.HTTP, s.HTTPNoConn, s.Lost.Total())
}

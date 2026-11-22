package internal_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	ibpf "iormesh/internal/bpf"
	"iormesh/internal/types"
)

// pokeJava is a JVM target in startScript's protocol: per line on stdin it
// opens and reads probePath through FileInputStream (two hooked natives,
// open0 and readBytes) and then asks ProcessHandle for its own info, which
// libjava's info0 reads from /proc with plain read(2)s — I/O of the very
// kind readBytes issues, from a native the mark does not probe: the JVM
// work the mark must not name.
const pokeJava = `import java.io.*;
public class Poke {
    public static void main(String[] args) throws Exception {
        BufferedReader in = new BufferedReader(new InputStreamReader(System.in));
        byte[] buf = new byte[64];
        while (in.readLine() != null) {
            try (FileInputStream f = new FileInputStream("` + probePath + `")) {
                f.read(buf);
            }
            ProcessHandle.current().info().commandLine();
            System.out.println("ok");
            System.out.flush();
        }
    }
}
`

// The cookies TestJavaMarkNamesTheNative gives the two natives.
const (
	cookieOpen0     = 7
	cookieReadBytes = 8
)

// TestJavaMarkNamesTheNative (M3, task td4): with the mark on and a JVM's
// FileInputStream natives probed, the JVM's open and read of probePath
// each come with a mark naming their native and its entry on
// CLOCK_MONOTONIC, sent right before the enter (same tid and time), while
// the /proc opens and reads the JVM issues outside those natives come
// with none; once detached, no native entered afterwards is marked.
func TestJavaMarkNamesTheNative(t *testing.T) {
	requireRoot(t)
	src := filepath.Join(t.TempDir(), "Poke.java")
	if err := os.WriteFile(src, []byte(pokeJava), 0o644); err != nil {
		t.Fatal(err)
	}
	java, err := exec.LookPath("java")
	if err != nil {
		t.Fatalf("no java on PATH: %v", err)
	}
	tg := startScript(t, java, src)
	tg.poke(t) // the JVM is up, libjava mapped
	tr := openTracerWith(t, ibpf.Config{Pids: []uint32{tg.pid()}, JavaMarks: true, NoAggregation: true})
	missing, err := tr.AttachJavaMarks(tg.pid(), []ibpf.JavaNative{
		{Symbol: "Java_java_io_FileInputStream_open0", Cookie: cookieOpen0},
		{Symbol: "Java_java_io_FileInputStream_readBytes", Cookie: cookieReadBytes},
		{Symbol: "Java_no_such_Class_native0", Cookie: 9},
	})
	if err != nil || len(missing) != 1 || missing[0] != "Java_no_such_Class_native0" {
		t.Fatalf("attach: missing %v, %v", missing, err)
	}
	tg.poke(t)
	checkMarks(t, collectMarks(tr, tg.pid(), time.Second))
	// Detached (in a session: once the JVM's thread group died), no
	// native entered afterwards is marked. A frame pushed before - the
	// main thread sits in readBytes on stdin, waiting for the next poke -
	// stays on its thread's mark, as no return probe will pop it; a live
	// JVM's join catches such a stale mark (its entry lies in a bracket
	// that closed before the syscall), a dead JVM's thread exits clear it.
	detached := monoNow()
	if err := tr.DetachJavaMarks(tg.pid()); err != nil {
		t.Fatalf("detach: %v", err)
	}
	tg.poke(t)
	for _, e := range collectMarks(tr, tg.pid(), time.Second) {
		if e.mark != nil && e.mark.Entry >= detached {
			t.Fatalf("%s marked by a native entered after the detach: %+v", e.kind, e.mark)
		}
	}
}

// markedEnter is one enter of the target with the mark sent right before
// it on its thread (nil: none).
type markedEnter struct {
	kind string // "openat:<path>", "read:<path>" ("read:?" for an fd opened before)
	mark *types.JavaMarkEvent
}

// markCollector pairs the target's openat and read enters with the mark
// record that came right before each on the same thread and time, and
// names each read by the path its fd was opened with (fd 0: "stdin").
type markCollector struct {
	pid     uint32
	out     []markedEnter
	last    map[uint32]*types.JavaMarkEvent // per tid
	opening map[uint32]string               // per tid: the openat in flight
	fds     map[int32]string
}

// collectMarks collects pid's marked enters for wait.
func collectMarks(tr *ibpf.Tracer, pid uint32, wait time.Duration) []markedEnter {
	c := &markCollector{pid: pid, last: map[uint32]*types.JavaMarkEvent{},
		opening: map[uint32]string{}, fds: map[int32]string{0: "stdin"}}
	deadline := time.After(wait)
	for {
		select {
		case batch, ok := <-tr.Records():
			if !ok {
				return c.out
			}
			for _, rec := range batch {
				c.file(rec)
			}
		case <-deadline:
			return c.out
		}
	}
}

// take returns tid's mark when it was sent for the enter at at.
func (c *markCollector) take(tid uint32, at uint64) *types.JavaMarkEvent {
	m := c.last[tid]
	delete(c.last, tid)
	if m != nil && m.Time != at {
		return nil
	}
	return m
}

// file files one record.
func (c *markCollector) file(rec any) {
	switch ev := rec.(type) {
	case *types.JavaMarkEvent:
		if ev.Pid == c.pid {
			cp := *ev
			c.last[ev.Tid] = &cp
		}
	case *types.OpenEvent:
		if ev.Pid == c.pid && ev.TraceId == types.SYS_ENTER_OPENAT {
			path := types.StringValue(ev.Filename[:])
			c.opening[ev.Tid] = path
			c.out = append(c.out, markedEnter{"openat:" + path, c.take(ev.Tid, ev.Time)})
		}
	case *types.RetEvent:
		if path, ok := c.opening[ev.Tid]; ok && ev.Pid == c.pid && ev.TraceId == types.SYS_EXIT_OPENAT {
			delete(c.opening, ev.Tid)
			if ev.Ret >= 0 {
				c.fds[int32(ev.Ret)] = path
			}
		}
	case *types.FdEvent:
		if ev.Pid == c.pid && ev.TraceId == types.SYS_ENTER_READ {
			path, ok := c.fds[ev.Fd]
			if !ok {
				path = "?"
			}
			c.out = append(c.out, markedEnter{"read:" + path, c.take(ev.Tid, ev.Time)})
		}
	}
}

// checkMarks: the open of probePath carries open0's mark and its read and
// stdin's carry readBytes'; the opens and reads of /proc files (info0's,
// a native the mark does not probe) carry none, and no read of another
// file carries readBytes'; every mark names a native entered before now;
// and a mark comes from a thread other than the launcher's (tid != pid),
// checked on its own because its failure has one known cause.
func checkMarks(t *testing.T, enters []markedEnter) {
	t.Helper()
	seen := map[string]bool{}
	for _, e := range enters {
		if m := e.mark; m != nil && (m.Entry == 0 || m.Entry > monoNow()) {
			t.Errorf("%s: mark %+v has no native entry before now", e.kind, m)
		}
		hooked := e.kind == "read:"+probePath || e.kind == "read:stdin" || e.kind == "openat:"+probePath
		proc := strings.HasPrefix(e.kind, "read:/proc/") || strings.HasPrefix(e.kind, "openat:/proc/")
		switch {
		case hooked && e.mark == nil:
			t.Errorf("%s: no mark", e.kind)
		case hooked:
			seen[e.kind] = e.mark.Cookie == map[bool]uint64{true: cookieOpen0, false: cookieReadBytes}[strings.HasPrefix(e.kind, "openat:")]
			seen["thread"] = seen["thread"] || e.mark.Tid != e.mark.Pid
		case proc && e.mark != nil:
			t.Errorf("%s: JVM work marked with cookie %d", e.kind, e.mark.Cookie)
		case proc:
			seen[strings.SplitN(e.kind, ":", 2)[0]+":/proc"] = true
		case e.mark != nil && e.mark.Cookie == cookieReadBytes:
			t.Errorf("%s: marked as readBytes'", e.kind)
		}
	}
	for _, k := range []string{"openat:" + probePath, "read:" + probePath, "read:stdin", "openat:/proc", "read:/proc"} {
		if !seen[k] {
			t.Errorf("no correctly marked %s; enters %+v", k, enters)
		}
	}
	// "thread": a hooked native marked on a thread whose tid is not the
	// pid. Java's main method never runs on the launcher thread, so on a
	// kernel whose uprobe_multi filters by thread (6.6-6.9 without commit
	// 46ba0e49) no mark is sent at all. Its own error names that cause, so
	// a reader of the failure does not have to infer it from the misses
	// above (nf4 review). Rocky 9.9's el9 kernel passes (task nf4).
	if !seen["thread"] {
		t.Errorf("no mark from a thread other than the launcher's (tid != pid): a kernel whose "+
			"uprobe_multi filters by thread instead of by process (6.6-6.9 without commit 46ba0e49) "+
			"drops every mark of a Java thread; enters %+v", enters)
	}
}

// monoNow reads CLOCK_MONOTONIC, the clock of the mark's entry stamps.
func monoNow() uint64 {
	var ts unix.Timespec
	_ = unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts)
	return uint64(ts.Nano())
}

// loopChunks is how many 16-byte chunks the loop file holds: well past the
// four individual calls of a key (rwagg.c, IORMESH_RW_INDIVIDUAL), so the
// later reads fold and the EOF read after them is held.
const loopChunks = 64

// loopJava is a JVM target in startScript's protocol: per line on stdin it
// reads the file args[0] names through FileInputStream in 16-byte chunks
// until EOF - a hot read loop whose every read(2), the EOF one included,
// comes from the readBytes native.
const loopJava = `import java.io.*;
public class Loop {
    public static void main(String[] args) throws Exception {
        BufferedReader in = new BufferedReader(new InputStreamReader(System.in));
        byte[] buf = new byte[16];
        while (in.readLine() != null) {
            try (FileInputStream f = new FileInputStream(args[0])) {
                while (f.read(buf) > 0) {
                }
            }
            System.out.println("ok");
            System.out.flush();
        }
    }
}
`

// heldEOF is one EOF read of the loop file the fold held: the enter it
// owed, sent at its exit, and the mark sent right before it (nil: none).
type heldEOF struct {
	enter *types.FdEvent
	mark  *types.JavaMarkEvent
}

// loopCollector follows the loop file's reads on the target: the fd its
// open returned, the latest record on that fd per tid (an enter, or a
// folded record), and the EOF reads whose enter came right after folded
// calls within the hot window - the held calls rwagg.c sends alone.
type loopCollector struct {
	markCollector
	path    string
	fd      int32
	pending map[uint32]*heldEOF // per tid: a read enter awaiting its exit
	lastAgg map[uint32]uint64   // per tid: end time of the record just before
	folded  int
	held    []heldEOF
}

// fileLoop files one record.
func (c *loopCollector) fileLoop(rec any) {
	switch ev := rec.(type) {
	case *types.RwAggregateEvent:
		if ev.Pid == c.pid && ev.Fd == c.fd && c.fd >= 0 {
			c.folded += int(ev.Count)
			c.lastAgg[ev.Tid] = ev.EndTime
		}
		return
	case *types.FdEvent:
		if ev.Pid == c.pid && ev.TraceId == types.SYS_ENTER_READ && ev.Fd == c.fd && c.fd >= 0 {
			cp := *ev
			mark := c.take(ev.Tid, ev.Time)
			if end, ok := c.lastAgg[ev.Tid]; ok && ev.Time <= end+uint64(time.Millisecond) {
				c.pending[ev.Tid] = &heldEOF{enter: &cp, mark: mark}
			}
			delete(c.lastAgg, ev.Tid)
			return
		}
	case *types.RetEvent:
		if h, ok := c.pending[ev.Tid]; ok && ev.Pid == c.pid && ev.TraceId == types.SYS_EXIT_READ {
			delete(c.pending, ev.Tid)
			if ev.Ret == 0 {
				c.held = append(c.held, *h)
			}
		}
	}
	c.file(rec)
	if fd, ok := c.fdOf(); ok {
		c.fd = fd
	}
}

// fdOf returns the fd the loop file's latest open returned.
func (c *loopCollector) fdOf() (int32, bool) {
	for fd, p := range c.fds {
		if p == c.path {
			delete(c.fds, fd)
			return fd, true
		}
	}
	return 0, false
}

// TestJavaMarkOnHeldRead (task ti4): with the fold on, a JVM thread inside
// FileInputStream.readBytes reads a file in many fast chunks; the reads
// after the fourth fold, so the EOF read is held at its enter and, not
// being hot (ret 0), sent alone at its exit. Its enter must still come
// with readBytes' mark stamped with its own enter time - else the last
// read of every hot Java loop counts as JVM work.
func TestJavaMarkOnHeldRead(t *testing.T) {
	requireRoot(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "Loop.java")
	data := filepath.Join(dir, "loop.dat")
	if err := os.WriteFile(src, []byte(loopJava), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(data, []byte(strings.Repeat("0123456789abcdef", loopChunks)), 0o644); err != nil {
		t.Fatal(err)
	}
	java, err := exec.LookPath("java")
	if err != nil {
		t.Fatalf("no java on PATH: %v", err)
	}
	tg := startScript(t, java, src, data)
	tg.poke(t) // the JVM is up, libjava mapped, Loop's methods warm
	tr := openTracerWith(t, ibpf.Config{Pids: []uint32{tg.pid()}, JavaMarks: true})
	if _, err := tr.AttachJavaMarks(tg.pid(), []ibpf.JavaNative{
		{Symbol: "Java_java_io_FileInputStream_readBytes", Cookie: cookieReadBytes},
	}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	for range 5 {
		tg.poke(t)
	}
	c := collectLoop(tr, tg.pid(), data, time.Second)
	if c.folded == 0 || len(c.held) == 0 {
		t.Fatalf("no held EOF read after folded calls (folded %d): the loop never folded", c.folded)
	}
	for _, h := range c.held {
		switch {
		case h.mark == nil:
			t.Errorf("held EOF read at %d sent without its Java mark", h.enter.Time)
		case h.mark.Cookie != cookieReadBytes || h.mark.Tid != h.enter.Tid:
			t.Errorf("held EOF read at %d: mark %+v is not readBytes' on its thread", h.enter.Time, h.mark)
		}
	}
}

// collectLoop collects the loop file's held EOF reads of pid for wait.
func collectLoop(tr *ibpf.Tracer, pid uint32, path string, wait time.Duration) *loopCollector {
	c := &loopCollector{
		markCollector: markCollector{pid: pid, last: map[uint32]*types.JavaMarkEvent{},
			opening: map[uint32]string{}, fds: map[int32]string{}},
		path: path, fd: -1, pending: map[uint32]*heldEOF{}, lastAgg: map[uint32]uint64{},
	}
	deadline := time.After(wait)
	for {
		select {
		case batch, ok := <-tr.Records():
			if !ok {
				return c
			}
			for _, rec := range batch {
				c.fileLoop(rec)
			}
		case <-deadline:
			return c
		}
	}
}

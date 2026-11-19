package internal_test

import (
	"os"
	"os/exec"
	"path/filepath"
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
// reads the JVM issues outside those natives come with none.
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
	marks := collectMarks(tr, tg.pid(), time.Second)
	checkMarks(t, marks)
}

// markedEnter is one enter of the target with the mark sent right before
// it on its thread (nil: none).
type markedEnter struct {
	kind string // "openat:<path>", "read"
	mark *types.JavaMarkEvent
}

// collectMarks pairs the target's openat and read enters with the mark
// record that came right before each on the same thread and time.
func collectMarks(tr *ibpf.Tracer, pid uint32, wait time.Duration) []markedEnter {
	var out []markedEnter
	last := map[uint32]*types.JavaMarkEvent{}
	deadline := time.After(wait)
	for {
		select {
		case batch, ok := <-tr.Records():
			if !ok {
				return out
			}
			for _, rec := range batch {
				out = pairMark(out, last, rec, pid)
			}
		case <-deadline:
			return out
		}
	}
}

// pairMark files one record for collectMarks.
func pairMark(out []markedEnter, last map[uint32]*types.JavaMarkEvent, rec any, pid uint32) []markedEnter {
	take := func(tid uint32, at uint64) *types.JavaMarkEvent {
		m := last[tid]
		delete(last, tid)
		if m != nil && m.Time != at {
			return nil
		}
		return m
	}
	switch ev := rec.(type) {
	case *types.JavaMarkEvent:
		if ev.Pid == pid {
			cp := *ev
			last[ev.Tid] = &cp
		}
	case *types.OpenEvent:
		if ev.Pid == pid && ev.TraceId == types.SYS_ENTER_OPENAT {
			out = append(out, markedEnter{"openat:" + types.StringValue(ev.Filename[:]), take(ev.Tid, ev.Time)})
		}
	case *types.FdEvent:
		if ev.Pid == pid && ev.TraceId == types.SYS_ENTER_READ {
			out = append(out, markedEnter{"read", take(ev.Tid, ev.Time)})
		}
	}
	return out
}

// checkMarks: the open of probePath carries open0's mark, some read
// readBytes' (probePath's, and stdin's), some read none (info0's), and
// every mark names a probed native entered before the syscall.
func checkMarks(t *testing.T, enters []markedEnter) {
	t.Helper()
	var openMarked, readMarked, readUnmarked bool
	for _, e := range enters {
		if m := e.mark; m != nil && (m.Entry == 0 || m.Entry > monoNow()) {
			t.Errorf("%s: mark %+v has no native entry before now", e.kind, m)
		}
		switch {
		case e.kind == "openat:"+probePath:
			openMarked = e.mark != nil && e.mark.Cookie == cookieOpen0
		case e.kind == "read" && e.mark == nil:
			readUnmarked = true
		case e.kind == "read" && e.mark.Cookie == cookieReadBytes:
			readMarked = true
		case e.kind == "read":
			t.Errorf("read marked with cookie %d", e.mark.Cookie)
		}
	}
	if !openMarked || !readMarked || !readUnmarked {
		t.Fatalf("open of %s marked %v, a read marked %v, a read unmarked %v; enters %+v",
			probePath, openMarked, readMarked, readUnmarked, enters)
	}
}

// monoNow reads CLOCK_MONOTONIC, the clock of the mark's entry stamps.
func monoNow() uint64 {
	var ts unix.Timespec
	_ = unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts)
	return uint64(ts.Nano())
}

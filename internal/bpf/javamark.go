package bpf

import (
	"errors"
	"fmt"
	"os"

	bpf "github.com/aquasecurity/libbpfgo"
)

// The Java mark's BPF side (internal/c/javamark.c, M3, task td4).
const (
	javaMarkFlagName  = "IORMESH_JAVA_MARK"
	javaMarkMapName   = "java_mark_map"
	javaMarkEnterProg = "iormesh_java_native_enter"
	javaMarkExitProg  = "iormesh_java_native_exit"
)

// JavaNative is one JDK native whose syscalls the mark names: its JNI
// symbol (agentspool.Hook.Symbol) and the cookie its uprobe carries, which
// a *types.JavaMarkEvent reports. Cookies are the caller's; 0 is reserved
// (the mark's "innermost native unknown").
type JavaNative struct {
	Symbol string
	Cookie uint64
}

// setJavaMarks sets the load-time switch and, when it is off, shrinks
// java_mark_map to one entry and keeps the uprobe programs from loading:
// a session without the Java join neither pays the map's memory nor
// needs a kernel with uprobe_multi links (Linux 6.6; the mark needs 6.10
// or a stable kernel with commit 46ba0e49, see JavaJoin's markCheck).
func (t *Tracer) setJavaMarks(on bool) error {
	flag := uint32(0)
	if on {
		flag = 1
	}
	if err := t.module.InitGlobalVariable(javaMarkFlagName, flag); err != nil {
		return fmt.Errorf("set %s: %w", javaMarkFlagName, err)
	}
	if on {
		return nil
	}
	m, err := t.module.GetMap(javaMarkMapName)
	if err != nil {
		return fmt.Errorf("get %s: %w", javaMarkMapName, err)
	}
	if err := m.SetMaxEntries(1); err != nil {
		return fmt.Errorf("shrink %s: %w", javaMarkMapName, err)
	}
	for _, name := range []string{javaMarkEnterProg, javaMarkExitProg} {
		prog, err := t.module.GetProgram(name)
		if err != nil {
			return fmt.Errorf("program %s: %w", name, err)
		}
		if err := prog.SetAutoload(false); err != nil {
			return fmt.Errorf("no autoload for %s: %w", name, err)
		}
	}
	return nil
}

// AttachJavaMarks attaches the mark's uprobes to the natives of the JVM
// pid (Config.JavaMarks must be on): a return and an entry probe per
// native, in the libraries of the JDK the JVM runs (openJavaLibs), scoped
// to pid so no other process traps. missing lists the natives found in
// none of the libraries: their syscalls carry no mark. A JVM attached
// before (a reused pid) has its old probes detached first. pid is the
// JVM's number in the tracer's PID namespace (pidns.go): /proc is read there
// and uprobe_multi looks the pid up in the caller's namespace. Call it from
// one goroutine, before Close.
func (t *Tracer) AttachJavaMarks(pid uint32, natives []JavaNative) (missing []string, err error) {
	if t.closed() {
		return nil, errClosed
	}
	files, err := openJavaLibs("/proc", pid)
	if err != nil {
		return nil, err
	}
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()
	_ = t.DetachJavaMarks(pid)
	found := map[string]bool{}
	for _, f := range files {
		offsets, cookies, names := nativeOffsets(f, natives)
		if len(offsets) == 0 {
			continue
		}
		if err := t.attachJavaLib(pid, f, offsets, cookies); err != nil {
			return nil, err
		}
		for _, n := range names {
			found[n] = true
		}
	}
	for _, n := range natives {
		if !found[n.Symbol] {
			missing = append(missing, n.Symbol)
		}
	}
	return missing, nil
}

// attachJavaLib attaches the return and the entry probe to one library's
// natives, by the descriptor openJavaLibs checked (/proc/self/fd/N), so
// the file probed is the file parsed. The return probe goes first: a
// native entered between the two attaches then runs the return probe
// without its push, which pops nothing but frames at or below its own
// stack pointer - never an outer native's (javamark.c). The reverse order
// would leave that native's push on the mark with no pop, and a later
// syscall of JVM work in the same still-open bracket would carry it.
func (t *Tracer) attachJavaLib(pid uint32, f *os.File, offsets, cookies []uint64) error {
	path := fmt.Sprintf("/proc/self/fd/%d", f.Fd())
	for _, p := range []struct {
		name string
		ret  bool
	}{{javaMarkExitProg, true}, {javaMarkEnterProg, false}} {
		prog, err := t.module.GetProgram(p.name)
		if err != nil {
			return fmt.Errorf("program %s: %v", p.name, err)
		}
		var link *bpf.BPFLink
		if p.ret {
			link, err = prog.AttachURetprobeMulti(int(pid), path, offsets, cookies)
		} else {
			link, err = prog.AttachUprobeMulti(int(pid), path, offsets, cookies)
		}
		if err != nil {
			return fmt.Errorf("attach %s to %s of pid %d: %w", p.name, f.Name(), pid, err)
		}
		t.addJavaLink(pid, link)
	}
	return nil
}

// addJavaLink keeps a JVM's uprobe link for DetachJavaMarks and Close:
// libbpfgo's Module.Close does not know uprobe_multi links.
func (t *Tracer) addJavaLink(pid uint32, link *bpf.BPFLink) {
	t.javaMu.Lock()
	defer t.javaMu.Unlock()
	if t.javaLinks == nil {
		t.javaLinks = map[uint32][]*bpf.BPFLink{}
	}
	t.javaLinks[pid] = append(t.javaLinks[pid], link)
}

// DetachJavaMarks detaches the mark's uprobes from the JVM pid once its
// process is gone: in a pattern session JVMs come and go, and each kept
// link would pin its process and two kernel uprobe consumers per native.
// Each link is destroyed once, error or not (a failed bpf_link__destroy
// has freed it already). A pid without links is no error.
func (t *Tracer) DetachJavaMarks(pid uint32) error {
	t.javaMu.Lock()
	links := t.javaLinks[pid]
	delete(t.javaLinks, pid)
	t.javaMu.Unlock()
	var err error
	for _, l := range links {
		err = errors.Join(err, l.Destroy())
	}
	return err
}

// detachAllJavaMarks is Close's: every JVM's links, before the module.
func (t *Tracer) detachAllJavaMarks() {
	t.javaMu.Lock()
	pids := make([]uint32, 0, len(t.javaLinks))
	for pid := range t.javaLinks {
		pids = append(pids, pid)
	}
	t.javaMu.Unlock()
	for _, pid := range pids {
		_ = t.DetachJavaMarks(pid)
	}
}

package correlate

import (
	"testing"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// pathCall feeds one path_event syscall (mkdir, unlink, statx, …).
func (f feeder) pathCall(enterID, exitID types.TraceId, dirfd int32, path string,
	flags uint32, ret int64) {
	ev := &types.PathEvent{EventType: types.ENTER_PATH_EVENT, TraceId: enterID,
		Time: f.tick(), Pid: f.pid, Tid: f.tid, Dirfd: dirfd, Flags: flags}
	if path == "" {
		ev.PathnameStatus = types.PATH_READ_NULL
	}
	copy(ev.Pathname[:], path)
	f.handle(ev)
	f.exit(exitID, ret)
}

// nameCall feeds one name_event syscall (rename, link, symlink).
func (f feeder) nameCall(enterID, exitID types.TraceId, oldDirfd int32, old string,
	newDirfd int32, newName string, ret int64) {
	ev := &types.NameEvent{EventType: types.ENTER_NAME_EVENT, TraceId: enterID,
		Time: f.tick(), Pid: f.pid, Tid: f.tid, Olddirfd: oldDirfd, Newdirfd: newDirfd}
	copy(ev.Oldname[:], old)
	copy(ev.Newname[:], newName)
	f.handle(ev)
	f.exit(exitID, ret)
}

func (f feeder) rename(old, newName string) {
	f.nameCall(types.SYS_ENTER_RENAME, types.SYS_EXIT_RENAME, atFDCWD, old, atFDCWD, newName, 0)
}

func (f feeder) ftruncate(fd int32) {
	f.fdCall(types.SYS_ENTER_FTRUNCATE, types.SYS_EXIT_FTRUNCATE, fd, 0)
}

// pathOf returns the args.path of the n-th event of kind ("" if unset).
func pathOf(t *testing.T, events []schema.Event, kind string, n int) string {
	t.Helper()
	evs := byKind(events, kind)
	if len(evs) <= n {
		t.Fatalf("%d %s events, want at least %d", len(evs), kind, n+1)
	}
	if evs[n].Args.Path == nil {
		return ""
	}
	return *evs[n].Args.Path
}

// TestRenameWhileOpen is file-lifecycle's point: an fd opened as a.dat
// names b.dat once a.dat was renamed — its metadata events say so, and the
// graph gains the process's opened edge to the new name.
func TestRenameWhileOpen(t *testing.T) {
	c, sink := newTest(t)
	f := feeder{t, c, 10, 11, new(uint64)}
	f.open("/d/a.dat", 193, 5)
	f.rename("/d/a.dat", "/d/b.dat")
	f.ftruncate(5)
	f.close(5)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	ren := byKind(sink.events, schema.KindRename)[0]
	if *ren.Args.Path != "/d/a.dat" || *ren.Args.NewPath != "/d/b.dat" || ren.FD != schema.NoFD {
		t.Fatalf("rename %+v: want path, new_path and no fd", ren)
	}
	if got := pathOf(t, sink.events, schema.KindFtruncate, 0); got != "/d/b.dat" {
		t.Fatalf("ftruncate after the rename names %q, want /d/b.dat", got)
	}
	proc := schema.ProcessNodeID("h1", 10, 1010)
	if !hasEdge(c.Graph(), schema.EdgeOpened, proc, schema.FileNodeID("h1", "/d/b.dat"), "") {
		t.Fatalf("no opened edge to the new name: %+v", c.Graph().Edges)
	}
}

// TestDirectoryRenameAndMove: renaming a directory moves the fds below it
// (in every traced process), a cross-directory move moves the one file,
// and a sibling sharing the name's prefix ("/d2") is left alone.
func TestDirectoryRenameAndMove(t *testing.T) {
	c, sink := newTest(t)
	f := feeder{t, c, 10, 11, new(uint64)}
	g := feeder{t, c, 20, 21, f.now}
	f.open("/d/sub/x.dat", 0, 5)
	g.open("/d/y.dat", 0, 6)
	f.open("/d2/z.dat", 0, 7)
	f.rename("/d/", "/e")
	f.rename("/e/sub/x.dat", "/f/x.dat")
	f.ftruncate(5)
	g.ftruncate(6)
	f.ftruncate(7)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"/f/x.dat", "/e/y.dat", "/d2/z.dat"} {
		if got := pathOf(t, sink.events, schema.KindFtruncate, i); got != want {
			t.Errorf("ftruncate %d names %q, want %q", i, got, want)
		}
	}
}

// TestUnlinkedNameDoesNotFollow: once a name was unlinked (or replaced by
// a rename onto it), a new file under that name moving away must not take
// the old file's fds along.
func TestUnlinkedNameDoesNotFollow(t *testing.T) {
	c, sink := newTest(t)
	f := feeder{t, c, 10, 11, new(uint64)}
	f.open("/d/a.dat", 0, 5)
	f.pathCall(types.SYS_ENTER_UNLINK, types.SYS_EXIT_UNLINK, atFDCWD, "/d/a.dat", 0, 0)
	f.open("/d/b.dat", 0, 6)
	f.open("/d/c.dat", 0, 7)
	f.rename("/d/c.dat", "/d/b.dat") // replaces the b.dat fd 6 holds
	f.rename("/d/a.dat", "/d/z.dat") // a new a.dat, not fd 5's
	f.rename("/d/b.dat", "/d/y.dat")
	for _, fd := range []int32{5, 6, 7} {
		f.ftruncate(fd)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"/d/a.dat", "/d/b.dat", "/d/y.dat"} {
		if got := pathOf(t, sink.events, schema.KindFtruncate, i); got != want {
			t.Errorf("ftruncate %d names %q, want %q", i, got, want)
		}
	}
}

// TestFailedRenameMovesNothing: only a successful rename changes names.
func TestFailedRenameMovesNothing(t *testing.T) {
	c, sink := newTest(t)
	f := feeder{t, c, 10, 11, new(uint64)}
	f.open("/d/a.dat", 0, 5)
	f.nameCall(types.SYS_ENTER_RENAME, types.SYS_EXIT_RENAME, atFDCWD, "/d/a.dat",
		atFDCWD, "/x/b.dat", -18) // EXDEV
	f.ftruncate(5)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if got := pathOf(t, sink.events, schema.KindFtruncate, 0); got != "/d/a.dat" {
		t.Fatalf("ftruncate names %q after a failed rename, want /d/a.dat", got)
	}
}

// TestDirfdRelativeCalls follows file-attrs' SecureDirectoryStream: a
// directory fd, its dup, a fchmodat and an openat relative to the dup —
// both resolved against the directory's path, fd the dirfd — and a
// utimensat with a NULL path, which names the fd's own file.
func TestDirfdRelativeCalls(t *testing.T) {
	c, sink := newTest(t)
	f := feeder{t, c, 10, 11, new(uint64)}
	f.open("/tmp/w", 0, 4)
	f.fdCall(types.SYS_ENTER_DUP, types.SYS_EXIT_DUP, 4, 5)
	f.pathCall(types.SYS_ENTER_FCHMODAT, types.SYS_EXIT_FCHMODAT, 5, "f.dat", 0, 0)
	ev := &types.OpenEvent{EventType: types.ENTER_OPEN_EVENT, TraceId: types.SYS_ENTER_OPENAT,
		Time: f.tick(), Pid: 10, Tid: 11, Dirfd: 5}
	copy(ev.Filename[:], "f.dat")
	f.handle(ev)
	f.exit(types.SYS_EXIT_OPENAT, 6)
	f.pathCall(types.SYS_ENTER_UTIMENSAT, types.SYS_EXIT_UTIMENSAT, 6, "", 0, 0)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	dup := byKind(sink.events, schema.KindDup)[0]
	chm := byKind(sink.events, schema.KindFchmodat)[0]
	if dup.FD != 4 || dup.Ret != 5 || chm.FD != 5 || chm.FDGen != 1 {
		t.Fatalf("dup %+v fchmodat %+v: want dup of fd 4, fchmodat on the new fd 5 (gen 1)", dup, chm)
	}
	if got := *chm.Args.Path; got != "/tmp/w/f.dat" {
		t.Errorf("fchmodat path %q, want /tmp/w/f.dat", got)
	}
	if got := pathOf(t, sink.events, schema.KindOpenat, 1); got != "/tmp/w/f.dat" {
		t.Errorf("dirfd-relative openat path %q, want /tmp/w/f.dat", got)
	}
	ut := byKind(sink.events, schema.KindUtimensat)[0]
	if ut.FD != 6 || ut.Args.Path == nil || *ut.Args.Path != "/tmp/w/f.dat" || ut.Args.Flags == nil {
		t.Errorf("utimensat %+v: want fd 6 naming /tmp/w/f.dat, with flags", ut)
	}
}

// TestPathKindArgs: AT_FDCWD is no fd, a relative path against it stays
// as given (the cwd is not tracked), flags appear only where the syscall
// has them, a symlink's text is never resolved, and creat is an open.
func TestPathKindArgs(t *testing.T) {
	c, sink := newTest(t)
	f := feeder{t, c, 10, 11, new(uint64)}
	f.pathCall(types.SYS_ENTER_MKDIR, types.SYS_EXIT_MKDIR, atFDCWD, "rel", 0o755, 0)
	f.pathCall(types.SYS_ENTER_STATX, types.SYS_EXIT_STATX, atFDCWD, "/l", 0x100, 0)
	f.open("/d", 0, 3)
	f.nameCall(types.SYS_ENTER_SYMLINKAT, types.SYS_EXIT_SYMLINKAT, atFDCWD, "t.dat", 3, "l", 0)
	f.pathCall(types.SYS_ENTER_CREAT, types.SYS_EXIT_CREAT, atFDCWD, "/d/n.dat", 0o644, 4)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	mk := byKind(sink.events, schema.KindMkdir)[0]
	if mk.FD != schema.NoFD || *mk.Args.Path != "rel" || mk.Args.Flags != nil {
		t.Errorf("mkdir %+v: want no fd, the raw relative path, no flags", mk)
	}
	if st := byKind(sink.events, schema.KindStatx)[0]; st.Args.Flags == nil || *st.Args.Flags != 0x100 {
		t.Errorf("statx %+v: want flags AT_SYMLINK_NOFOLLOW", st)
	}
	sl := byKind(sink.events, schema.KindSymlinkat)[0]
	if *sl.Args.Path != "t.dat" || *sl.Args.NewPath != "/d/l" {
		t.Errorf("symlinkat %+v: want text t.dat, link /d/l", sl)
	}
	cr := byKind(sink.events, schema.KindCreat)[0]
	if cr.FD != 4 || cr.FDGen != 1 || *cr.Args.Flags != creatFlags || *cr.Args.Path != "/d/n.dat" {
		t.Errorf("creat %+v: want fd 4 gen 1 with O_CREAT|O_WRONLY|O_TRUNC", cr)
	}
}

// TestPositionalIOBytes: pread64 and writev count bytes like read and
// write; xattr reads do not (their ret is a value's size, not file data).
func TestPositionalIOBytes(t *testing.T) {
	c, sink := newTest(t)
	f := feeder{t, c, 10, 11, new(uint64)}
	f.open("/d/a", 0, 3)
	f.fdCall(types.SYS_ENTER_PREAD64, types.SYS_EXIT_PREAD64, 3, 100)
	f.fdCall(types.SYS_ENTER_WRITEV, types.SYS_EXIT_WRITEV, 3, 7)
	f.fdCall(types.SYS_ENTER_FGETXATTR, types.SYS_EXIT_FGETXATTR, 3, 8)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	pr, wv := byKind(sink.events, schema.KindPread64)[0], byKind(sink.events, schema.KindWritev)[0]
	gx := byKind(sink.events, schema.KindFgetxattr)[0]
	if pr.Bytes != 100 || wv.Bytes != 7 || gx.Bytes != 0 || *gx.Args.Path != "/d/a" {
		t.Fatalf("pread64 %+v writev %+v fgetxattr %+v", pr, wv, gx)
	}
}

// TestNameFixupSecondSlot: a new name whose enter-time read faulted is
// recovered by the exit-time re-read (slot 2) before the exit.
func TestNameFixupSecondSlot(t *testing.T) {
	c, sink := newTest(t)
	f := feeder{t, c, 10, 11, new(uint64)}
	ev := &types.NameEvent{EventType: types.ENTER_NAME_EVENT, TraceId: types.SYS_ENTER_LINK,
		Time: f.tick(), Pid: 10, Tid: 11, Olddirfd: atFDCWD, Newdirfd: atFDCWD,
		NewnameStatus: types.PATH_READ_FAILED}
	copy(ev.Oldname[:], "/a")
	f.handle(ev)
	fix := &types.OpenNameFixupEvent{EventType: types.OPEN_NAME_FIXUP_EVENT,
		TraceId: types.SYS_ENTER_LINK, Tid: 11, Slot: types.OPEN_NAME_FIXUP_SLOT_SECOND}
	copy(fix.Filename[:], "/b")
	f.handle(fix)
	f.exit(types.SYS_EXIT_LINK, 0)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if l := byKind(sink.events, schema.KindLink)[0]; l.Args.NewPath == nil || *l.Args.NewPath != "/b" {
		t.Fatalf("link %+v: want new_path /b from the fixup", l)
	}
}

// TestGoldenFileLifecycle feeds file-lifecycle's strace (FileLifecycle.java)
// through the correlator and the shipped golden, whose ftruncate is
// declared on the renamed path: the offline twin of the integration test.
func TestGoldenFileLifecycle(t *testing.T) {
	c, sink := newTest(t)
	f := feeder{t, c, 10, 12, new(uint64)}
	d := tmp + "/file-lifecycle.d"
	mkdir := func(p string) { f.pathCall(types.SYS_ENTER_MKDIR, types.SYS_EXIT_MKDIR, atFDCWD, p, 0o777, 0) }
	rmdir := func(p string) { f.pathCall(types.SYS_ENTER_RMDIR, types.SYS_EXIT_RMDIR, atFDCWD, p, 0, 0) }
	f.open("/iormesh-marker/file-lifecycle/begin", 0, -2)
	mkdir(d)
	f.open(d+"/a.dat", 193, 5)
	f.write(5, 512)
	f.rename(d+"/a.dat", d+"/b.dat")
	f.write(5, 512)
	f.ftruncate(5)
	f.close(5)
	f.open(d+"/b.dat", 0, 5)
	f.pathCall(types.SYS_ENTER_UTIMENSAT, types.SYS_EXIT_UTIMENSAT, 5, "", 0, 0)
	f.close(5)
	mkdir(d + "/sub")
	f.rename(d+"/b.dat", d+"/sub/c.dat")
	f.pathCall(types.SYS_ENTER_UNLINK, types.SYS_EXIT_UNLINK, atFDCWD, d+"/sub/c.dat", 0, 0)
	rmdir(d + "/sub")
	rmdir(d)
	f.open("/iormesh-marker/file-lifecycle/end", 0, -2)
	checkGolden(t, c, sink, "file-lifecycle")
}

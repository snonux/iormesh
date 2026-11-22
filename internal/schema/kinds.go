package schema

import "slices"

// kinds lists every event kind of schema v1, sorted: what KnownKind
// accepts, spelled out for the readers that must name them all (query
// -event's usage error, task wh4). TestKindsListsEveryKind holds it to
// the package's Kind* constants, so a kind added there without here
// fails the test.
var kinds = func() []string {
	k := []string{
		KindOpen, KindOpenat, KindOpenat2, KindRead, KindWrite, KindClose,
		KindConnect, KindAccept, KindAccept4, KindTCPState, KindMarker,
		KindDrop, KindSession, KindEnd, KindExec, KindFork, KindAttach,
		KindCreat, KindMkdir, KindMkdirat, KindMknod, KindMknodat, KindRmdir,
		KindUnlink, KindUnlinkat, KindRename, KindRenameat, KindRenameat2,
		KindLink, KindLinkat, KindSymlink, KindSymlinkat, KindReadlink,
		KindReadlinkat, KindNewstat, KindNewlstat, KindNewfstat,
		KindNewfstatat, KindStatx, KindTruncate, KindFtruncate,
		KindFallocate, KindUtime, KindUtimes, KindFutimesat, KindUtimensat,
		KindChmod, KindFchmod, KindFchmodat, KindFchmodat2, KindChown,
		KindLchown, KindFchown, KindFchownat, KindSetxattr, KindLsetxattr,
		KindFsetxattr, KindGetxattr, KindLgetxattr, KindFgetxattr,
		KindListxattr, KindLlistxattr, KindFlistxattr, KindRemovexattr,
		KindLremovexattr, KindFremovexattr, KindPread64, KindPwrite64,
		KindReadv, KindWritev, KindPreadv, KindPwritev, KindPreadv2,
		KindPwritev2, KindDup, KindDup2, KindDup3, KindFcntl,
		KindBind, KindSendto, KindRecvfrom, KindSendmsg, KindRecvmsg,
		KindSendmmsg, KindRecvmmsg, KindPipe, KindPipe2,
		KindSocketpair, KindKill, KindTgkill, KindTkill, KindSignal,
		KindEpollCreate, KindEpollCreate1, KindEpollCtl, KindEpollWait,
		KindEpollPwait, KindEpollPwait2, KindPoll, KindPpoll,
		KindSendfile64, KindSplice, KindTee, KindCopyFileRange,
		KindHTTP, KindDNS,
	}
	slices.Sort(k)
	return k
}()

// Kinds returns every event kind of schema v1, sorted; the slice is the
// caller's.
func Kinds() []string {
	return slices.Clone(kinds)
}

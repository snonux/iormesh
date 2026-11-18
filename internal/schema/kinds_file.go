package schema

// The M2 file surface (PLAN.md M2, "What we trace"; decision #7: the file
// family is on by default). Each kind is the kernel tracepoint's syscall
// name, as for the M1 kinds — "newfstatat", not strace's "fstatat" — so a
// kind always says which syscall fired. Appended, never renamed: a trace
// written by an older tracer must keep validating.
//
// Args per shape (docs/schema.md "Event kinds (M2 file surface)"):
//   - path kinds: args.path, resolved against the dirfd's path when it is
//     relative and the dirfd was opened in the trace; fd is the dirfd (or
//     the file itself for an empty path), -1 for AT_FDCWD;
//   - name kinds (rename, link, symlink): args.path is the old name (for
//     symlink the link's text, never resolved), args.new_path the new one;
//   - fd kinds: fd; the metadata ones also carry the fd's current path,
//     which follows renames (M2 step 5).
//
// args.flags is set only where the syscall has a flags argument (statx's
// AT_SYMLINK_NOFOLLOW, linkat's AT_SYMLINK_FOLLOW, …).
const (
	// Create and delete.
	KindCreat   = "creat"
	KindMkdir   = "mkdir"
	KindMkdirat = "mkdirat"
	KindMknod   = "mknod"
	KindMknodat = "mknodat"
	KindRmdir   = "rmdir"
	KindUnlink  = "unlink"
	// KindUnlinkat covers rmdir's AT_REMOVEDIR form too; the record does
	// not carry the flag, so the kind cannot tell the two apart.
	KindUnlinkat = "unlinkat"

	// Rename and links: path is the old name, new_path the new one.
	KindRename     = "rename"
	KindRenameat   = "renameat"
	KindRenameat2  = "renameat2"
	KindLink       = "link"
	KindLinkat     = "linkat"
	KindSymlink    = "symlink"
	KindSymlinkat  = "symlinkat"
	KindReadlink   = "readlink"
	KindReadlinkat = "readlinkat"

	// The stat family.
	KindNewstat    = "newstat"
	KindNewlstat   = "newlstat"
	KindNewfstat   = "newfstat"
	KindNewfstatat = "newfstatat"
	KindStatx      = "statx"

	// Size and times.
	KindTruncate  = "truncate"
	KindFtruncate = "ftruncate"
	KindFallocate = "fallocate"
	KindUtime     = "utime"
	KindUtimes    = "utimes"
	KindFutimesat = "futimesat"
	KindUtimensat = "utimensat"

	// Permissions and ownership.
	KindChmod     = "chmod"
	KindFchmod    = "fchmod"
	KindFchmodat  = "fchmodat"
	KindFchmodat2 = "fchmodat2"
	KindChown     = "chown"
	KindLchown    = "lchown"
	KindFchown    = "fchown"
	KindFchownat  = "fchownat"

	// Extended attributes.
	KindSetxattr     = "setxattr"
	KindLsetxattr    = "lsetxattr"
	KindFsetxattr    = "fsetxattr"
	KindGetxattr     = "getxattr"
	KindLgetxattr    = "lgetxattr"
	KindFgetxattr    = "fgetxattr"
	KindListxattr    = "listxattr"
	KindLlistxattr   = "llistxattr"
	KindFlistxattr   = "flistxattr"
	KindRemovexattr  = "removexattr"
	KindLremovexattr = "lremovexattr"
	KindFremovexattr = "fremovexattr"

	// Positional and vectored data I/O: bytes like read and write.
	KindPread64  = "pread64"
	KindPwrite64 = "pwrite64"
	KindReadv    = "readv"
	KindWritev   = "writev"
	KindPreadv   = "preadv"
	KindPwritev  = "pwritev"
	KindPreadv2  = "preadv2"
	KindPwritev2 = "pwritev2"

	// Descriptor copies: fd is the old descriptor, ret the new one; the
	// new instance names the old one's file and socket (M2 step 6). The
	// file-attrs scenario needs dup (its SecureDirectoryStream dups the
	// directory fd). dup3 carries its flags (O_CLOEXEC).
	KindDup  = "dup"
	KindDup2 = "dup2"
	KindDup3 = "dup3"
	// KindFcntl carries args.cmd. Its F_DUPFD and F_DUPFD_CLOEXEC forms are
	// copies like dup (ret is the new fd); F_SETFD and F_GETFD tell the
	// correlator an fd's close-on-exec state, which exec needs (M2 step 6).
	KindFcntl = "fcntl"
)

// fileKind reports whether kind is one of the M2 file-surface kinds.
func fileKind(kind string) bool {
	switch kind {
	case KindCreat, KindMkdir, KindMkdirat, KindMknod, KindMknodat, KindRmdir,
		KindUnlink, KindUnlinkat,
		KindRename, KindRenameat, KindRenameat2, KindLink, KindLinkat,
		KindSymlink, KindSymlinkat, KindReadlink, KindReadlinkat,
		KindNewstat, KindNewlstat, KindNewfstat, KindNewfstatat, KindStatx,
		KindTruncate, KindFtruncate, KindFallocate,
		KindUtime, KindUtimes, KindFutimesat, KindUtimensat,
		KindChmod, KindFchmod, KindFchmodat, KindFchmodat2,
		KindChown, KindLchown, KindFchown, KindFchownat,
		KindSetxattr, KindLsetxattr, KindFsetxattr, KindGetxattr, KindLgetxattr,
		KindFgetxattr, KindListxattr, KindLlistxattr, KindFlistxattr,
		KindRemovexattr, KindLremovexattr, KindFremovexattr,
		KindPread64, KindPwrite64, KindReadv, KindWritev,
		KindPreadv, KindPwritev, KindPreadv2, KindPwritev2,
		KindDup, KindDup2, KindDup3, KindFcntl:
		return true
	}
	return false
}

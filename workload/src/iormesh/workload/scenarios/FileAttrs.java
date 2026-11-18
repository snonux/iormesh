package iormesh.workload.scenarios;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
import java.io.IOException;
import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.nio.file.DirectoryStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.SecureDirectoryStream;
import java.nio.file.attribute.PosixFileAttributeView;
import java.nio.file.attribute.PosixFilePermission;
import java.nio.file.attribute.PosixFilePermissions;
import java.nio.file.attribute.UserDefinedFileAttributeView;
import java.util.List;
import java.util.Map;
import java.util.Set;

/**
 * M2's metadata ops: permissions, ownership and xattrs, by path and by
 * fd. Options: {@code --dir} (default: a fresh temp directory). Runs as
 * any user: chown sets the file's own uid and gid, which the kernel lets
 * the owner do without CAP_CHOWN, so the syscalls fire and succeed with
 * no privilege. xattrs are user.* ones; the filesystem under --dir must
 * support them (tmpfs does since Linux 6.6, ext4/xfs/btrfs always).
 * Syscalls on the calling thread, in order (strace, JDK 25 running the
 * --release 21 classes; stat calls and the NSS lookup are undeclared):
 *
 * <pre>
 *   openat(F, O_WRONLY|O_CREAT|O_TRUNC) = fd1; write(fd1, 16); close(fd1)
 *                                          F = $tmp/file-attrs.dat
 *   chmod(F, 0600)                         setPosixFilePermissions
 *   chown(F, uid, -1); chown(F, -1, gid)   setAttribute unix:uid / unix:gid
 *   openat($tmp, O_RDONLY) = fd2; dup(fd2) = fd3     SecureDirectoryStream
 *   fchmodat(fd3, "file-attrs.dat", 0640)            its view's setPermissions
 *   (getpwuid: /etc/passwd or the NSS daemon)        its view's getOwner
 *   openat(fd3, "file-attrs.dat", O_RDONLY) = fd4; fchown(fd4, uid, -1); close(fd4)
 *   close(fd2); close(fd3)
 *   per xattr op: openat(F, O_RDONLY) = fdN; f*xattr(fdN, "user.iormesh"); close(fdN)
 *     for fsetxattr, fgetxattr (size probe), fgetxattr, flistxattr, fremovexattr
 *   unlink(F)
 * </pre>
 *
 * There is no fchmod(2) the JDK reaches: the fd-relative permission
 * change it offers is fchmodat on a directory fd. fchown(2) does fire.
 * Every xattr op opens the file anew, so each gets its own fd instance.
 *
 * expect.json leaves the dirfd-relative openat (fd4) undeclared: its
 * path is "file-attrs.dat" relative to fd3, and whether the trace shows
 * it raw or resolved is the M2 correlator's call; a declaration without
 * a path would bind greedily to the NSS lookup's open just before it.
 * fchown's fd binds fd4 instead.
 */
public final class FileAttrs implements Scenario {
    static final String NAME = "file-attrs";
    static final String XATTR = "iormesh";
    static final String XATTR_VALUE = "scenario";

    @Override
    public String name() {
        return NAME;
    }

    @Override
    public void run(String role, Map<String, String> opts) throws Exception {
        Options.requireRole(NAME, role, Set.of("solo"));
        Options.requireKnown(opts, Set.of(Workdir.OPTION));
        try (Workdir wd = Workdir.open(opts)) {
            Path f = wd.file(NAME + ".dat");
            try {
                Files.write(f, "file-attrs data\n".getBytes(StandardCharsets.US_ASCII));
                byPath(f);
                byFd(f);
                xattrs(f);
            } finally {
                Files.deleteIfExists(f);
            }
        }
    }

    /** chmod and chown on the path, as most tools issue them. */
    private static void byPath(Path f) throws IOException {
        Set<PosixFilePermission> rw = PosixFilePermissions.fromString("rw-------");
        Files.setPosixFilePermissions(f, rw);
        // unix:uid and unix:gid map to chown(path, uid, -1) and
        // chown(path, -1, gid) — no name lookup, so no NSS traffic
        // (passwd files, sssd sockets) lands inside the bracket.
        int uid = (Integer) Files.getAttribute(f, "unix:uid");
        int gid = (Integer) Files.getAttribute(f, "unix:gid");
        Files.setAttribute(f, "unix:uid", uid);
        Files.setAttribute(f, "unix:gid", gid);
        if (!Files.getPosixFilePermissions(f).equals(rw)) {
            throw new IOException("chmod did not apply: " + f);
        }
    }

    /**
     * The fd-relative forms, through a SecureDirectoryStream's attribute
     * view (the race-free API the JDK offers): setPermissions is fchmodat
     * on the stream's directory fd, setOwner opens the file relative to
     * that fd and fchowns it. Opening the stream also dups its directory
     * fd, a second dup(2) for M2 to see.
     */
    private static void byFd(Path f) throws IOException {
        Set<PosixFilePermission> rwr = PosixFilePermissions.fromString("rw-r-----");
        try (DirectoryStream<Path> ds = Files.newDirectoryStream(f.getParent())) {
            if (!(ds instanceof SecureDirectoryStream<Path> sds)) {
                throw new IOException("no SecureDirectoryStream on this platform");
            }
            PosixFileAttributeView v = sds.getFileAttributeView(f.getFileName(),
                    PosixFileAttributeView.class);
            v.setPermissions(rwr);
            v.setOwner(v.getOwner());
        }
        if (!Files.getPosixFilePermissions(f).equals(rwr)) {
            throw new IOException("fchmod did not apply: " + f);
        }
    }

    /** Sets, reads, lists and removes one user.* xattr. */
    private static void xattrs(Path f) throws IOException {
        UserDefinedFileAttributeView v = Files.getFileAttributeView(f,
                UserDefinedFileAttributeView.class);
        try {
            v.write(XATTR, StandardCharsets.US_ASCII.encode(XATTR_VALUE));
        } catch (IOException e) {
            throw new IOException("user xattrs unsupported under " + f.getParent()
                    + " (use a --dir on ext4/xfs/btrfs/tmpfs)", e);
        }
        ByteBuffer back = ByteBuffer.allocate(v.size(XATTR));
        v.read(XATTR, back);
        back.flip();
        if (!StandardCharsets.US_ASCII.decode(back).toString().equals(XATTR_VALUE)) {
            throw new IOException("xattr read back differs: " + f);
        }
        List<String> names = v.list();
        if (!names.equals(List.of(XATTR))) {
            throw new IOException("xattr list is " + names + ": " + f);
        }
        v.delete(XATTR);
    }
}

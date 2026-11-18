import java.io.ByteArrayOutputStream;
import java.io.DataInputStream;
import java.io.DataOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Enumeration;
import java.util.zip.ZipEntry;
import java.util.zip.ZipFile;

/**
 * Build tool for `mage agentBuild` (run with the JDK's source launcher:
 * {@code java agent/tools/Relocate.java <in.jar> <out dir> <from> <to>}).
 *
 * Shades a dependency the way Maven's shade plugin would, without Maven:
 * every class of the jar is written under the new package, and every
 * class-file reference to the old one is rewritten. A class file names
 * other classes only through CONSTANT_Utf8 entries of its constant pool
 * (class names, descriptors, signatures), and all other entries refer to
 * those by index, so rewriting the Utf8 strings and copying the rest of
 * the file byte for byte relocates the class. {@code from} and {@code to}
 * are internal-form prefixes ("org/objectweb/asm/").
 *
 * The jar's module-info.class and META-INF are dropped: the shaded copy
 * lives in the agent jar's unnamed module, and the jar's manifest is not
 * the agent's.
 */
public final class Relocate {
    private Relocate() {}

    public static void main(String[] args) throws IOException {
        if (args.length != 4) {
            throw new IllegalArgumentException("usage: Relocate <in.jar> <out dir> <from> <to>");
        }
        Path out = Path.of(args[1]);
        int n = 0;
        try (ZipFile jar = new ZipFile(args[0])) {
            Enumeration<? extends ZipEntry> entries = jar.entries();
            while (entries.hasMoreElements()) {
                ZipEntry e = entries.nextElement();
                if (!isRelocatable(e, args[2])) {
                    continue;
                }
                byte[] cls;
                try (InputStream in = jar.getInputStream(e)) {
                    cls = relocate(in.readAllBytes(), args[2], args[3]);
                }
                Path dst = out.resolve(args[3] + e.getName().substring(args[2].length()));
                Files.createDirectories(dst.getParent());
                Files.write(dst, cls);
                n++;
            }
        }
        if (n == 0) {
            throw new IOException("no classes under " + args[2] + " in " + args[0]);
        }
    }

    /** Only the dependency's own classes move; everything else is dropped. */
    private static boolean isRelocatable(ZipEntry e, String from) {
        return !e.isDirectory() && e.getName().startsWith(from)
                && e.getName().endsWith(".class");
    }

    /** Rewrites the constant pool's Utf8 entries and copies the rest as is. */
    static byte[] relocate(byte[] cls, String from, String to) throws IOException {
        DataInputStream in = new DataInputStream(new java.io.ByteArrayInputStream(cls));
        ByteArrayOutputStream buf = new ByteArrayOutputStream(cls.length + 1024);
        DataOutputStream out = new DataOutputStream(buf);
        out.writeInt(in.readInt());          // magic
        out.writeInt(in.readInt());          // minor, major
        int count = in.readUnsignedShort();
        out.writeShort(count);
        for (int i = 1; i < count; i++) {
            i += copyEntry(in, out, from, to);
        }
        in.transferTo(out);                  // everything after the pool
        return buf.toByteArray();
    }

    /**
     * Copies one constant-pool entry, relocating it if it is Utf8. Returns
     * 1 for Long and Double, which take two pool slots (JVMS 4.4.5).
     */
    private static int copyEntry(DataInputStream in, DataOutputStream out,
                                 String from, String to) throws IOException {
        int tag = in.readUnsignedByte();
        out.writeByte(tag);
        switch (tag) {
            case 1 -> {
                byte[] raw = in.readNBytes(in.readUnsignedShort());
                // Modified UTF-8 never encodes a non-ASCII char with ASCII
                // bytes, so replacing the ASCII prefix on the raw bytes'
                // ISO-8859-1 view is exact and keeps every other byte.
                String s = new String(raw, StandardCharsets.ISO_8859_1).replace(from, to);
                byte[] rel = s.getBytes(StandardCharsets.ISO_8859_1);
                if (rel.length > 0xffff) {
                    throw new IOException("relocated Utf8 constant too long");
                }
                out.writeShort(rel.length);
                out.write(rel);
            }
            case 7, 8, 16, 19, 20 -> out.write(in.readNBytes(2));
            case 15 -> out.write(in.readNBytes(3));
            case 3, 4, 9, 10, 11, 12, 17, 18 -> out.write(in.readNBytes(4));
            case 5, 6 -> {
                out.write(in.readNBytes(8));
                return 1;
            }
            default -> throw new IOException("unknown constant pool tag " + tag);
        }
        return 0;
    }
}

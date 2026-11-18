package iormesh.s1.agent;

import java.util.List;
import java.util.Set;
import java.util.stream.Collectors;

/**
 * Throwaway (S1, task wa4): the Java-level hook set for file-basic and
 * tcp-loopback on JDK 21/25. Every entry is a *Java* method that calls a
 * JNI I/O native directly; a native itself has no body to rewrite, so
 * where the native is called straight from a bigger method (Net.accept,
 * Net.listen) the caller is hooked instead. One id per class.method,
 * covering all overloads.
 */
final class HookTable {
    private static final List<String> IDS = List.of(
            "java/io/FileOutputStream.open",         // -> open0
            "java/io/FileOutputStream.write",        // -> write, writeBytes
            "java/io/FileInputStream.open",          // -> open0
            "java/io/FileInputStream.read",          // -> read0, readBytes
            "java/io/FileDescriptor.close",          // -> close0
            "java/io/RandomAccessFile.readBytes",    // -> readBytes0
            "java/io/RandomAccessFile.seek",         // -> seek0
            "sun/nio/fs/UnixNativeDispatcher.unlink", // -> unlink0
            "sun/nio/ch/Net.socket",                 // -> socket0
            "sun/nio/ch/Net.serverSocket",           // -> socket0
            "sun/nio/ch/Net.bind",                   // -> bind0
            "sun/nio/ch/Net.connect",                // -> connect0
            "sun/nio/ch/NioSocketImpl.listen",       // -> Net.listen (native)
            "sun/nio/ch/NioSocketImpl.accept",       // -> Net.accept (native)
            "sun/nio/ch/SocketDispatcher.read",      // -> read0
            "sun/nio/ch/SocketDispatcher.write",     // -> write0
            "sun/nio/ch/UnixDispatcher.close");      // -> close0

    static final Set<String> CLASSES = IDS.stream()
            .map(s -> s.substring(0, s.lastIndexOf('.')))
            .collect(Collectors.toUnmodifiableSet());

    private HookTable() {}

    static List<String> ids() {
        return IDS;
    }
}

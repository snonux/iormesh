package iormesh.agent;

import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.InputStream;
import java.io.OutputStream;
import java.io.RandomAccessFile;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.attribute.BasicFileAttributes;

/**
 * The target of `mage agentTest`'s smoke run under each JDK: does a
 * little of every hook kind of the tables (file streams,
 * RandomAccessFile, Files stat/move/delete, a TCP loopback connection
 * with a shutdown) so the agent's debug dump shows its
 * hooks fired on that JDK. Compiled for 17 like the agent, unlike the
 * workload (21), so it also runs on the 17 variant's JDK. Prints "ok".
 */
public final class AgentSmoke {
    private AgentSmoke() {}

    public static void main(String[] args) throws Exception {
        Path dir = Files.createTempDirectory("iormesh-agent-smoke");
        Path file = dir.resolve("smoke.dat");
        files(file);
        // stat, rename (same directory, so a rename(2), not a copy), unlink.
        Files.readAttributes(file, BasicFileAttributes.class);
        Path moved = Files.move(file, dir.resolve("moved.dat"));
        Files.delete(moved);
        Files.delete(dir);
        loopback();
        System.out.println("ok");
    }

    private static void files(Path file) throws Exception {
        try (OutputStream out = new FileOutputStream(file.toFile())) {
            out.write(new byte[4096]);
        }
        try (InputStream in = new FileInputStream(file.toFile())) {
            if (in.read(new byte[4096]) != 4096) {
                throw new IllegalStateException("short read");
            }
        }
        try (RandomAccessFile raf = new RandomAccessFile(file.toFile(), "rw")) {
            raf.seek(100);
            raf.write(new byte[16]);
            raf.seek(0);
            raf.readFully(new byte[16]);
        }
    }

    /** One connection on 127.0.0.1: socket, bind, listen, accept, connect, I/O, shutdown. */
    private static void loopback() throws Exception {
        try (ServerSocket server = new ServerSocket(0, 1, InetAddress.getLoopbackAddress())) {
            try (Socket client = new Socket()) {
                client.connect(new InetSocketAddress(server.getInetAddress(), server.getLocalPort()));
                try (Socket peer = server.accept()) {
                    client.getOutputStream().write(42);
                    if (peer.getInputStream().read() != 42) {
                        throw new IllegalStateException("loopback byte lost");
                    }
                    client.shutdownOutput();
                    if (peer.getInputStream().read() != -1) {
                        throw new IllegalStateException("no EOF after shutdownOutput");
                    }
                }
            }
        }
    }
}

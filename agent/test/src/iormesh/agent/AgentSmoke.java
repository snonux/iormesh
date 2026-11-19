package iormesh.agent;

import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.InputStream;
import java.io.OutputStream;
import java.io.RandomAccessFile;
import com.sun.net.httpserver.HttpServer;
import java.net.HttpURLConnection;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.attribute.BasicFileAttributes;

/**
 * The target of `mage agentTest`'s smoke run under each JDK: does a
 * little of every hook kind of the tables (file streams,
 * RandomAccessFile, Files stat/move/delete, a TCP loopback connection
 * with a shutdown) and one request through each HTTP hook's library (the
 * third-party clients Apache HttpClient and OkHttp too: task de4), so
 * the agent's debug dump shows its hooks fired on that JDK. Compiled for
 * 17 like the agent, unlike the workload (21), so it also runs on the 17
 * variant's JDK. Prints "ok".
 */
public final class AgentSmoke {
    /** Apache HttpClient 5 and its dependencies, vendored for the workload (task de4). */
    static final Path APACHE_JARS = Path.of("workload", "libs", "third_party", "httpclient5");
    /** OkHttp 5, Okio and the Kotlin standard library, vendored likewise. */
    static final Path OKHTTP_JARS = Path.of("workload", "libs", "third_party", "okhttp");

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
        http();
        System.out.println("ok");
    }

    /**
     * One request each through the HTTP hooks' libraries (decision #22):
     * the JDK's HttpServer answers the JDK's HttpClient (send and
     * sendAsync) and an
     * HttpURLConnection, so the debug dump shows the http hooks applied
     * and recording on this JDK (with spool=, which the smoke passes).
     * The handler ends its exchanges by closing the body alone, never
     * HttpExchange.close(): the server records them when it returns (the
     * workload's HttpServe closes, the integration tests cover that).
     */
    private static void http() throws Exception {
        HttpServer server = HttpServer.create(
                new InetSocketAddress(InetAddress.getLoopbackAddress(), 0), 1);
        server.createContext("/", ex -> {
            byte[] body = "hi".getBytes(StandardCharsets.UTF_8);
            ex.sendResponseHeaders(200, body.length);
            try (OutputStream out = ex.getResponseBody()) {
                out.write(body);
            }
        });
        server.start();
        try {
            URI uri = URI.create("http://127.0.0.1:" + server.getAddress().getPort() + "/smoke?k=v");
            HttpClient client = HttpClient.newBuilder().version(HttpClient.Version.HTTP_1_1).build();
            HttpResponse<String> r = client.send(HttpRequest.newBuilder(uri).build(),
                    HttpResponse.BodyHandlers.ofString());
            // sendAsync (task wd4): recorded from the completing thread.
            if (client.sendAsync(HttpRequest.newBuilder(uri).build(),
                    HttpResponse.BodyHandlers.ofString()).get().statusCode() != 200) {
                throw new IllegalStateException("http smoke: sendAsync");
            }
            // A name lookup (task wd4): one DNS record for localhost.
            InetAddress.getAllByName("localhost");
            HttpURLConnection c = (HttpURLConnection) uri.toURL().openConnection();
            if (r.statusCode() != 200 || c.getResponseCode() != 200) {
                throw new IllegalStateException("http smoke: " + r.statusCode());
            }
            c.getInputStream().readAllBytes();
            c.disconnect();
            apache(uri);
            okhttp(uri);
        } finally {
            server.stop(0);
        }
    }

    /**
     * One GET through Apache HttpClient 5 (task de4), the vendored jars of
     * the workload's third-party module in a class loader of their own —
     * an application loader the agent has to find and check, like a web
     * app's — and called reflectively (the agent's tests compile without
     * them), so the dump shows the third-party hooks applied and recording
     * on this JDK. Run from the repository root, as `mage agentTest` does.
     */
    private static void apache(URI uri) throws Exception {
        try (java.net.URLClassLoader l = jars(APACHE_JARS)) {
            Class<?> clients = l.loadClass("org.apache.hc.client5.http.impl.classic.HttpClients");
            Object client = clients.getMethod("createDefault").invoke(null);
            Object get = l.loadClass("org.apache.hc.client5.http.classic.methods.HttpGet")
                    .getConstructor(URI.class).newInstance(uri);
            Class<?> req = l.loadClass("org.apache.hc.core5.http.ClassicHttpRequest");
            Object resp = client.getClass().getMethod("execute", req).invoke(client, get);
            int code = (int) l.loadClass("org.apache.hc.core5.http.HttpResponse")
                    .getMethod("getCode").invoke(resp);
            ((AutoCloseable) resp).close();
            ((AutoCloseable) client).close();
            if (code != 200) {
                throw new IllegalStateException("http smoke: apache " + code);
            }
        }
    }

    /**
     * One GET through OkHttp 5 the same way: Kotlin-compiled targets, which
     * the agent's store scan must accept and its rewrite keep verifiable,
     * on this JDK.
     */
    private static void okhttp(URI uri) throws Exception {
        try (java.net.URLClassLoader l = jars(OKHTTP_JARS)) {
            Class<?> clientType = l.loadClass("okhttp3.OkHttpClient");
            Object client = clientType.getConstructor().newInstance();
            Class<?> builder = l.loadClass("okhttp3.Request$Builder");
            Object b = builder.getConstructor().newInstance();
            builder.getMethod("url", String.class).invoke(b, uri.toString());
            Object req = builder.getMethod("build").invoke(b);
            Object call = clientType.getMethod("newCall", l.loadClass("okhttp3.Request"))
                    .invoke(client, req);
            Object resp = l.loadClass("okhttp3.Call").getMethod("execute").invoke(call);
            int code = (int) resp.getClass().getMethod("code").invoke(resp);
            ((AutoCloseable) resp).close();
            Object pool = clientType.getMethod("connectionPool").invoke(client);
            pool.getClass().getMethod("evictAll").invoke(pool);
            Object dispatcher = clientType.getMethod("dispatcher").invoke(client);
            ((java.util.concurrent.ExecutorService) dispatcher.getClass()
                    .getMethod("executorService").invoke(dispatcher)).shutdown();
            if (code != 200) {
                throw new IllegalStateException("http smoke: okhttp " + code);
            }
        }
    }

    /** A loader of its own for the jars in dir (the app's parent loader above it). */
    private static java.net.URLClassLoader jars(Path dir) throws Exception {
        java.util.List<java.net.URL> urls = new java.util.ArrayList<>();
        try (java.util.stream.Stream<Path> jars = Files.list(dir)) {
            for (Path p : (Iterable<Path>) jars.sorted()::iterator) {
                if (p.toString().endsWith(".jar")) {
                    urls.add(p.toUri().toURL());
                }
            }
        }
        return new java.net.URLClassLoader(urls.toArray(new java.net.URL[0]),
                AgentSmoke.class.getClassLoader());
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

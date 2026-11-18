package iormesh.agent;

import java.io.BufferedReader;
import java.io.IOException;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.lang.instrument.Instrumentation;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;

/**
 * Which JDK methods the agent rewrites, as data: one row per hooked
 * method — (id, kind, class, method, descriptor, native, allowed
 * syscalls) — read from a tab-separated resource,
 * {@code hooks-<variant>.tsv} next to this class.
 *
 * Data rather than code so that decision #29's per-JDK differences are
 * table variants (S1: 17 lacks UnixFileDispatcherImpl/UnixDispatcher and
 * has native RAF readBytes/writeBytes; 21 to 24 keep Net.serverSocket's
 * stream flag, which 25 dropped — found on a real 21, S1 had only its
 * sources), and so
 * the tracer can load the same rows for its uprobe set and per-native
 * syscall allowlist (M3 step 3). Exact descriptors, not names: a JDK
 * that changes a signature leaves the row unmatched, which the agent
 * reports, instead of silently bracketing a different method.
 */
final class HookTable {
    /** One hooked method; owner is an internal name (java/io/…). */
    record Hook(int id, String kind, String owner, String method, String descriptor,
                String nativeRef, List<String> syscalls) {
        String key() {
            return key(owner, method, descriptor);
        }

        static String key(String owner, String method, String descriptor) {
            return owner + "." + method + descriptor;
        }
    }

    private static final int COLUMNS = 7;
    /**
     * The I/O kinds a row may name (the tables' header lists them): a
     * typo would otherwise become a kind nothing downstream knows.
     */
    static final Set<String> KINDS = Set.of("open", "read", "write", "close", "seek",
            "socket", "bind", "connect", "listen", "accept", "shutdown", "unlink",
            "rename", "stat");

    private final String variant;
    private final List<Hook> hooks;
    private final Map<String, Hook> byKey = new HashMap<>();
    private final Set<String> classes = new HashSet<>();
    private final int maxId;

    private HookTable(String variant, List<Hook> hooks) {
        this.variant = variant;
        this.hooks = Collections.unmodifiableList(hooks);
        int max = 0;
        for (Hook h : hooks) {
            if (byKey.putIfAbsent(h.key(), h) != null) {
                throw new IllegalArgumentException(variant + ": duplicate method " + h.key());
            }
            classes.add(h.owner());
            max = Math.max(max, h.id());
        }
        this.maxId = max;
    }

    /**
     * The variant for a JDK feature release: "jdk17" for 17–20 (only 17
     * is checked; 18–20 are assumed to match it), "jdk21" for 21–24 (21
     * is run; 22 and 24 were checked by javap, 23 is assumed), "jdk25"
     * from 25 on, null below 17 — such JVMs are traced without Java
     * attribution (decision #29).
     */
    static String variantFor(int feature) {
        if (feature < 17) {
            return null;
        }
        if (feature < 21) {
            return "jdk17";
        }
        return feature < 25 ? "jdk21" : "jdk25";
    }

    /** Loads the bundled variant's resource. */
    static HookTable load(String variant) throws IOException {
        String res = "hooks-" + variant + ".tsv";
        try (InputStream in = HookTable.class.getResourceAsStream(res)) {
            if (in == null) {
                throw new IOException("no hook table " + res);
            }
            return parse(variant, new BufferedReader(
                    new InputStreamReader(in, StandardCharsets.UTF_8)).lines().toList());
        }
    }

    /** Parses table lines; '#' lines and blank lines are comments. */
    static HookTable parse(String variant, List<String> lines) {
        List<Hook> hooks = new ArrayList<>();
        Set<Integer> ids = new HashSet<>();
        for (int n = 0; n < lines.size(); n++) {
            String line = lines.get(n);
            if (line.isBlank() || line.startsWith("#")) {
                continue;
            }
            Hook h = parseRow(variant + ":" + (n + 1), line);
            if (!ids.add(h.id())) {
                throw new IllegalArgumentException(variant + ":" + (n + 1)
                        + ": duplicate id " + h.id());
            }
            hooks.add(h);
        }
        return new HookTable(variant, hooks);
    }

    private static Hook parseRow(String where, String line) {
        String[] f = line.split("\t", -1);
        if (f.length != COLUMNS) {
            throw new IllegalArgumentException(where + ": want " + COLUMNS
                    + " tab-separated columns, got " + f.length);
        }
        for (String col : f) {
            if (col.isEmpty()) {
                throw new IllegalArgumentException(where + ": empty column");
            }
        }
        int id;
        try {
            id = Integer.parseInt(f[0]);
        } catch (NumberFormatException e) {
            throw new IllegalArgumentException(where + ": bad id " + f[0], e);
        }
        // 0 is not an id: Hooks.counts() index 0 stays unused.
        if (id <= 0 || id > Short.MAX_VALUE) {
            throw new IllegalArgumentException(where + ": id out of range " + id);
        }
        if (!KINDS.contains(f[1])) {
            throw new IllegalArgumentException(where + ": unknown kind " + f[1]);
        }
        if (!f[4].startsWith("(")) {
            throw new IllegalArgumentException(where + ": bad descriptor " + f[4]);
        }
        return new Hook(id, f[1], f[2], f[3], f[4], f[5], List.of(f[6].split(",")));
    }

    String variant() {
        return variant;
    }

    List<Hook> hooks() {
        return hooks;
    }

    /** The largest id in the table; Hooks is configured with maxId()+1. */
    int maxId() {
        return maxId;
    }

    boolean hasClass(String internalName) {
        return classes.contains(internalName);
    }

    /** The hook for one method, or null if the method is not listed. */
    Hook lookup(String owner, String method, String descriptor) {
        return byKey.get(Hook.key(owner, method, descriptor));
    }

    /**
     * The listed classes the JVM has already loaded (by the bootstrap
     * loader, which owns every listed class) — those must be
     * retransformed; the rest are rewritten when they load.
     */
    Class<?>[] loadedTargets(Instrumentation inst) {
        List<Class<?>> out = new ArrayList<>();
        for (Class<?> c : inst.getAllLoadedClasses()) {
            if (c.getClassLoader() == null && hasClass(c.getName().replace('.', '/'))
                    && inst.isModifiableClass(c)) {
                out.add(c);
            }
        }
        return out.toArray(new Class<?>[0]);
    }
}

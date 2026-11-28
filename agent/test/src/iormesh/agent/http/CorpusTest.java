package iormesh.agent.http;

import iormesh.agent.AgentTests;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.HashSet;
import java.util.List;
import java.util.Set;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * The redaction corpus (task qp4), shared with internal/redact's
 * TestCorpus: the same file, read from the repository root where mage
 * runs the tests. The agent copies only the header and URL rules, so it
 * runs the header and url cases; the argv cases are the tracer's alone.
 * A masked case fails when its SEKRET canary survives, a limit case when
 * the canary disappears (the limit got fixed: the line and
 * docs/schema/redaction.md change with it). The file's header comment
 * documents the format.
 */
public final class CorpusTest {
    static final Path CORPUS = Path.of("internal", "redact", "testdata", "corpus.tsv");
    static final Path SPACE = Path.of("internal", "redact", "testdata", "space.txt");
    static final String CANARY = "SEKRET";
    static final Pattern CODE_POINT = Pattern.compile("<U\\+([0-9A-F]{4})>");

    private CorpusTest() {}

    public static List<AgentTests.Case> cases() {
        return List.of(new AgentTests.Case("http/redaction corpus", CorpusTest::corpus),
                new AgentTests.Case("http/redaction white space", CorpusTest::spaceTable));
    }

    /**
     * Redaction.space equals Go's unicode.IsSpace for every char (task
     * xp4): internal/redact/testdata/space.txt lists the code points Go
     * reports, and internal/redact's TestSpaceTable keeps it so.
     */
    static void spaceTable() throws Exception {
        Set<Integer> go = new HashSet<>();
        for (String line : Files.readAllLines(SPACE, StandardCharsets.UTF_8)) {
            String l = line.strip();
            if (!l.isEmpty() && !l.startsWith("#")) {
                go.add(Integer.parseInt(l, 16));
            }
        }
        AgentTests.check(!go.isEmpty(), SPACE + ": no code point");
        List<String> diff = new ArrayList<>();
        for (int c = 0; c <= 0xFFFF; c++) {
            if (Redaction.space((char) c) != go.contains(c)) {
                diff.add(String.format("%04X", c));
            }
        }
        AgentTests.check(diff.isEmpty(), "Redaction.space differs from " + SPACE + " at " + diff);
    }

    static void corpus() throws Exception {
        List<String> lines = Files.readAllLines(CORPUS, StandardCharsets.UTF_8);
        List<String> failures = new ArrayList<>();
        int ran = 0;
        for (int n = 1; n <= lines.size(); n++) {
            String[] f = lines.get(n - 1).split("\t", -1);
            if (f.length < 4 || !(f[0].equals("header") || f[0].equals("url"))) {
                continue;   // a comment, a blank line, or an argv case
            }
            ran++;
            String failure = verdict(f);
            if (failure != null) {
                failures.add(CORPUS + ":" + n + " (" + f[2] + "): " + failure);
            }
        }
        AgentTests.check(ran > 0, CORPUS + ": no header or url case");
        AgentTests.check(failures.isEmpty(), String.join("\n", failures));
    }

    /**
     * An input field with its "&lt;TAB&gt;" and "&lt;U+XXXX&gt;" escapes
     * (task xp4) turned into the characters they stand for, as
     * internal/redact's unescapeCorpus does.
     */
    static String unescape(String s) {
        return CODE_POINT.matcher(s.replace("<TAB>", "\t")).replaceAll(
                m -> Matcher.quoteReplacement(Character.toString(Integer.parseInt(m.group(1), 16))));
    }

    /** null when the case holds, else what went wrong. */
    static String verdict(String[] f) {
        String[] in = Arrays.copyOfRange(f, 3, f.length);
        for (int i = 0; i < in.length; i++) {
            in[i] = unescape(in[i]);
        }
        String out = f[0].equals("header") ? Redaction.header(in[0], in[1]) : Redaction.url(in[0]);
        boolean leaked = out.contains(CANARY);
        boolean limit = f[1].startsWith("limit:");
        if (!limit && leaked) {
            return "leaked: " + out;
        }
        if (limit && !leaked) {
            return f[1] + " is masked now: " + out + "; make it a masked line and update "
                    + "docs/schema/redaction.md";
        }
        return null;
    }
}

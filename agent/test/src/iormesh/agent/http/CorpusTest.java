package iormesh.agent.http;

import iormesh.agent.AgentTests;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;

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
    static final String CANARY = "SEKRET";

    private CorpusTest() {}

    public static List<AgentTests.Case> cases() {
        return List.of(new AgentTests.Case("http/redaction corpus", CorpusTest::corpus));
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

    /** null when the case holds, else what went wrong. */
    static String verdict(String[] f) {
        String[] in = Arrays.copyOfRange(f, 3, f.length);
        for (int i = 0; i < in.length; i++) {
            in[i] = in[i].replace("<TAB>", "\t");
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

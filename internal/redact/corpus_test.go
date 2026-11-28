package redact

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The redaction corpus (task qp4): real secret carriers, shared with the
// Java agent's CorpusTest, which reads the header and url cases. Its
// header comment documents the format. Every audit before it found new
// leaks because the tests held only the forms each fix thought of; the
// corpus collects what real programs put on a command line or in a
// header, and its accepted limits are checked against the doc so the
// two stay equal.
const (
	corpusFile = "testdata/corpus.tsv"
	limitsDoc  = "../../docs/schema/redaction.md"
	canary     = "SEKRET"
)

// corpusCase is one corpus line.
type corpusCase struct {
	line  int
	kind  string   // argv, header or url
	limit string   // the accepted limit's tag; "" when masked is expected
	note  string   // where the form comes from, or why it is a limit
	input []string // argv elements; header name and value; one URL
}

// readCorpus parses the corpus file, failing the test on a malformed line.
func readCorpus(t *testing.T) []corpusCase {
	t.Helper()
	f, err := os.Open(corpusFile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }() // read-only: a close error loses nothing
	var cases []corpusCase
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		c, err := parseCorpusLine(n, line)
		if err != nil {
			t.Fatalf("%s:%d: %v", corpusFile, n, err)
		}
		cases = append(cases, c)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return cases
}

// inputCount is how many input fields each kind takes; 0 means one or
// more (argv).
var inputCount = map[string]int{"argv": 0, "header": 2, "url": 1}

// parseCorpusLine reads "kind, expect, note, input..." (TAB-separated).
func parseCorpusLine(n int, line string) (corpusCase, error) {
	f := strings.Split(line, "\t")
	if len(f) < 4 {
		return corpusCase{}, fmt.Errorf("want kind, expect, note and input, got %d fields", len(f))
	}
	c := corpusCase{line: n, kind: f[0], note: f[2], input: f[3:]}
	want, ok := inputCount[c.kind]
	if !ok {
		return c, fmt.Errorf("unknown kind %q", c.kind)
	}
	if want != 0 && len(c.input) != want {
		return c, fmt.Errorf("%s takes %d inputs, got %d", c.kind, want, len(c.input))
	}
	if tag, isLimit := strings.CutPrefix(f[1], "limit:"); isLimit && tag != "" {
		c.limit = tag
	} else if f[1] != "masked" {
		return c, fmt.Errorf("expect %q: want masked or limit:<tag>", f[1])
	}
	for i := range c.input {
		c.input[i] = unescapeCorpus(c.input[i])
	}
	if !strings.Contains(strings.Join(c.input, "\t"), canary) {
		return c, fmt.Errorf("input holds no %s canary", canary)
	}
	return c, nil
}

// codePoint is the corpus's "<U+XXXX>" escape (task xp4).
var codePoint = regexp.MustCompile(`<U\+([0-9A-F]{4})>`)

// unescapeCorpus turns an input field's "<TAB>" and "<U+XXXX>" escapes
// into the runes they stand for, as the Java agent's CorpusTest does.
func unescapeCorpus(s string) string {
	s = strings.ReplaceAll(s, "<TAB>", "\t")
	return codePoint.ReplaceAllStringFunc(s, func(m string) string {
		r, _ := strconv.ParseUint(m[3:7], 16, 32) // four hex digits: cannot fail
		return string(rune(r))
	})
}

// redact runs the case's input through p as its kind says.
func (c corpusCase) redact(p Policy) string {
	switch c.kind {
	case "header":
		return p.Header(c.input[0], c.input[1])
	case "url":
		return p.URL(c.input[0])
	}
	return strings.Join(p.Argv(c.input), "\t")
}

// TestCorpus fails when a masked case's canary survives (a leak) and when
// a limit case's canary disappears: the limit got fixed, so the line
// becomes a masked one and docs/schema/redaction.md loses the limit.
func TestCorpus(t *testing.T) {
	p := Default()
	for _, c := range readCorpus(t) {
		out := c.redact(p)
		leaked := strings.Contains(out, canary)
		switch {
		case c.limit == "" && leaked:
			t.Errorf("%s:%d (%s): leaked: %q → %q", corpusFile, c.line, c.note, c.input, out)
		case c.limit != "" && !leaked:
			t.Errorf("%s:%d (%s): limit %q is masked now: %q → %q; make it a masked line "+
				"and update %s", corpusFile, c.line, c.note, c.limit, c.input, out, limitsDoc)
		}
	}
}

// limitRow is a row of the doc's "Accepted limits" table: "| `tag` | …".
var limitRow = regexp.MustCompile("^\\| `([a-z-]+)` \\|")

// docLimits returns the tags of the "Accepted limits" table in doc, in
// order: the rows between that heading and the next one.
func docLimits(doc string) []string {
	var tags []string
	in := false
	for line := range strings.Lines(doc) {
		if strings.HasPrefix(line, "#") {
			in = strings.TrimSpace(strings.TrimLeft(line, "#")) == "Accepted limits"
			continue
		}
		if m := limitRow.FindStringSubmatch(line); in && m != nil {
			tags = append(tags, m[1])
		}
	}
	return tags
}

// TestCorpusLimitsDocumented keeps docs/schema/redaction.md's accepted
// limits equal to the corpus's: every limit tag has a row in the doc's
// table, and every row has at least one corpus case that proves it.
func TestCorpusLimitsDocumented(t *testing.T) {
	b, err := os.ReadFile(limitsDoc)
	if err != nil {
		t.Fatal(err)
	}
	doc := docLimits(string(b))
	var corpus []string
	for _, c := range readCorpus(t) {
		if c.limit != "" && !slices.Contains(corpus, c.limit) {
			corpus = append(corpus, c.limit)
		}
	}
	slices.Sort(doc)
	slices.Sort(corpus)
	if !slices.Equal(doc, corpus) {
		t.Errorf("%s's Accepted limits = %q, the corpus's limit tags = %q", limitsDoc, doc, corpus)
	}
}

// TestParseCorpusLineRejects pins the parser's errors, so a malformed
// corpus line fails loudly instead of testing nothing.
func TestParseCorpusLineRejects(t *testing.T) {
	for _, line := range []string{
		"argv\tmasked\tnote",                     // no input
		"cmd\tmasked\tnote\tSEKRET",              // unknown kind
		"header\tmasked\tnote\tX-A",              // header without a value
		"argv\tmaybe\tnote\tSEKRET",              // unknown expect
		"argv\tlimit:\tnote\tSEKRET",             // limit without a tag
		"argv\tmasked\tnote\t--password\tsecret", // no canary
	} {
		if _, err := parseCorpusLine(1, line); err == nil {
			t.Errorf("parseCorpusLine(%q) = nil error", line)
		}
	}
}

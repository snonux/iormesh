package eval

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// repoFile is a path from the repository root.
func repoFile(rel string) string { return filepath.Join("..", "..", filepath.FromSlash(rel)) }

// TestQuestionSetGrounded: the shipped set loads strictly and every
// answer_contains and answer_sequence string is in the scenario's
// declarations — the answers come from expect.json, never from what a
// model happened to say.
func TestQuestionSetGrounded(t *testing.T) {
	s, err := LoadSet(repoFile("eval/questions/microservices.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	expect, err := os.ReadFile(repoFile("workload/scenarios/" + s.Scenario + "/expect.json"))
	if err != nil {
		t.Fatal(err)
	}
	decl := strings.ToLower(string(expect))
	for _, q := range s.Questions {
		for _, want := range slices.Concat(q.Contains, q.Sequence) {
			if !strings.Contains(decl, strings.ToLower(want)) {
				t.Errorf("%s: %q is not in the %s declarations", q.ID, want, s.Scenario)
			}
		}
	}
	if s.Threshold != 0.8 || len(s.Questions) != 10 {
		t.Errorf("threshold %v over %d questions; the pinned set is 0.8 over 10", s.Threshold, len(s.Questions))
	}
}

// TestLoadSetRejects: a set that could not score is refused, a
// misspelt key included.
func TestLoadSetRejects(t *testing.T) {
	const ok = "scenario: s\nthreshold: 0.5\nquestions:\n  - id: a\n    question: q?\n"
	for name, tc := range map[string]struct{ yaml, want string }{
		"unknown key":      {ok + "    answer_contain: [x]\n", "answer_contain"},
		"no matcher":       {ok, "no matcher"},
		"negative only":    {ok + "    answer_excludes: [x]\n    answer_not_regex: y\n", "no positive matcher"},
		"empty matcher":    {ok + "    answer_contains: ['']\n", "empty matcher"},
		"bad regex":        {ok + "    answer_regex: '('\n", "answer_regex"},
		"bad not regex":    {ok + "    answer_regex: x\n    answer_not_regex: '('\n", "answer_not_regex"},
		"zero threshold":   {strings.Replace(ok, "0.5", "0", 1) + "    answer_regex: x\n", "threshold"},
		"no scenario":      {strings.Replace(ok, "scenario: s", "scenario: ''", 1) + "    answer_regex: x\n", "no scenario"},
		"duplicate id":     {ok + "    answer_regex: x\n  - id: a\n    question: q?\n    answer_regex: x\n", "duplicate"},
		"no question text": {strings.Replace(ok, "q?", "' '", 1) + "    answer_regex: x\n", "no question"},
		"unless alone":     {ok + "    answer_regex: x\n    answer_excludes_unless: [plain]\n", "answer_excludes_unless"},
		"empty unless":     {ok + "    answer_regex: x\n    answer_excludes: [y]\n    answer_excludes_unless: ['']\n", "empty matcher"},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "q.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadSet(path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err %v, want one saying %q", err, tc.want)
			}
		})
	}
}

// TestScore: each matcher, case-insensitive, and the failures it
// reports.
func TestScore(t *testing.T) {
	seq := Question{Sequence: []string{"gateway", "orders", "inventory", "billing"}}
	unless := Question{Excludes: []string{"/inventory/"}, ExcludesUnless: []string{"plain", "unencrypted"}}
	for name, tc := range map[string]struct {
		q      Question
		answer string
		fails  int
	}{
		"contains":                  {Question{Contains: []string{"DataStore#save"}}, "it was datastore#SAVE", 0},
		"contains missing":          {Question{Contains: []string{"DataStore#save", "tmp"}}, "DataStore#save", 1},
		"sequence in order":         {seq, "gateway -> orders -> inventory -> billing -> inventory", 0},
		"sequence reordered":        {seq, "gateway, orders, billing, inventory", 1},
		"sequence repeated":         {Question{Sequence: []string{"a", "a"}}, "a", 1},
		"excluded present":          {Question{Excludes: []string{"withdraw"}}, "ServiceNode#withdraw", 1},
		"excluded absent":           {Question{Excludes: []string{"withdraw"}}, "ServiceNode#retire", 0},
		"regex":                     {Question{Regex: `tmp[\s\S]*renam`}, "wrote x.TMP\nthen RENAMED it", 0},
		"regex fails":               {Question{Regex: `tmp[\s\S]*renam`}, "renamed, then tmp", 1},
		"not regex matches":         {Question{NotRegex: `/orders\b`}, "/inventory/reserve, /orders", 1},
		"not regex prefix":          {Question{NotRegex: `/orders\b`}, "/ordersx only", 0},
		"every matcher fails":       {Question{Contains: []string{"x"}, Excludes: []string{"y"}, Regex: "z"}, "y", 3},
		"unless exempts its clause": {unless, "/orders is https; /inventory/reserve went unencrypted", 0},
		"unless per sentence":       {unless, "/orders is https. /inventory/ was PLAIN.\n", 0},
		"unless elsewhere":          {unless, "/inventory/reserve is https; /stock is plain", 1},
		"unless no word":            {unless, "/orders and /inventory/reserve are https", 1},
		"unless every clause":       {unless, "/inventory/a is plain. /inventory/b is https", 1},
		// The words match whole words: "explain" holds no "plain".
		"unless inside a word": {unless, "/inventory/reserve went over TLS as the logs explain", 1},
		// An abbreviation's "." followed by a space splits too, so the
		// word must sit on the same side of it as the path (pinned).
		"unless split at e.g.": {unless, "the plain ones, e.g. /inventory/reserve", 1},
		"unless before e.g.":   {unless, "/inventory/reserve was plain, e.g. no TLS", 0},
	} {
		if got := tc.q.Score(tc.answer); len(got) != tc.fails {
			t.Errorf("%s: failures %v, want %d", name, got, tc.fails)
		}
	}
}

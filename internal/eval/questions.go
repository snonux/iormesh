package eval

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Set is one question file: the scenario its trace comes from, the
// pass threshold pinned with the questions (docs/milestones.md M5: "pinned
// when the question set is written"), and the questions.
type Set struct {
	Scenario  string     `yaml:"scenario"`
	Threshold float64    `yaml:"threshold"`
	Questions []Question `yaml:"questions"`
}

// Question is one question and the matchers its answer is scored by.
// Every matcher set must hold; all compare case-insensitively. At least
// one positive matcher (Contains, Sequence or Regex) is required: the
// exclusions only reject wrong answers, not empty ones.
type Question struct {
	ID       string `yaml:"id"`
	Question string `yaml:"question"`
	// Contains must all appear in the answer.
	Contains []string `yaml:"answer_contains"`
	// Sequence must appear in the answer in this order (each after the
	// previous one's end).
	Sequence []string `yaml:"answer_sequence"`
	// Excludes must not appear: the wrong answers a guess would give.
	Excludes []string `yaml:"answer_excludes"`
	// ExcludesUnless narrows Excludes to a clause (split at ";", a line
	// end or a sentence's "."): an excluded string fails the answer only
	// in a clause holding none of these words. It lets an answer name a
	// wrong candidate as wrong — "/inventory/reserve went unencrypted"
	// for "which paths went over TLS" (task fp4) — while a clause that
	// claims it still fails.
	ExcludesUnless []string `yaml:"answer_excludes_unless"`
	// Regex must match the answer (RE2, matched case-insensitively).
	Regex string `yaml:"answer_regex"`
	// NotRegex must not match the answer: an exclusion a substring cannot
	// say (a whole path, not every path it prefixes).
	NotRegex string `yaml:"answer_not_regex"`
}

// LoadSet reads a question file strictly — an unknown key is an error,
// so a misspelt matcher cannot silently match everything — and checks
// it (Validate).
func LoadSet(path string) (Set, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Set{}, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var s Set
	if err := dec.Decode(&s); err != nil {
		return Set{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := s.Validate(); err != nil {
		return Set{}, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// Validate rejects a set that cannot score: no scenario, a threshold
// outside (0, 1], no questions, a question without an id, text or
// positive matcher (answer_contains, answer_sequence or answer_regex —
// exclusions alone pass an empty answer), answer_excludes_unless without
// answer_excludes, a duplicate id, an empty matcher string or a bad
// regex.
func (s Set) Validate() error {
	switch {
	case s.Scenario == "":
		return errors.New("no scenario")
	case s.Threshold <= 0 || s.Threshold > 1:
		return fmt.Errorf("threshold %v: want 0 < threshold <= 1", s.Threshold)
	case len(s.Questions) == 0:
		return errors.New("no questions")
	}
	seen := map[string]bool{}
	for _, q := range s.Questions {
		if seen[q.ID] {
			return fmt.Errorf("question %q: duplicate id", q.ID)
		}
		seen[q.ID] = true
		if err := q.validate(); err != nil {
			return fmt.Errorf("question %q: %w", q.ID, err)
		}
	}
	return nil
}

func (q Question) validate() error {
	switch {
	case q.ID == "":
		return errors.New("no id")
	case strings.TrimSpace(q.Question) == "":
		return errors.New("no question")
	case len(q.Contains)+len(q.Sequence)+len(q.Excludes) == 0 && q.Regex == "" && q.NotRegex == "":
		return errors.New("no matcher: the answer could not be scored")
	case len(q.Contains)+len(q.Sequence) == 0 && q.Regex == "":
		// Exclusions alone only say what a wrong answer holds: an empty
		// answer or "I do not know" holds none of it and would pass.
		return errors.New("no positive matcher (answer_contains, answer_sequence or " +
			"answer_regex): an empty answer would pass")
	case len(q.ExcludesUnless) > 0 && len(q.Excludes) == 0:
		return errors.New("answer_excludes_unless without answer_excludes: it narrows nothing")
	}
	for _, list := range [][]string{q.Contains, q.Sequence, q.Excludes, q.ExcludesUnless} {
		for _, s := range list {
			if strings.TrimSpace(s) == "" {
				return errors.New("an empty matcher string matches every answer")
			}
		}
	}
	for name, re := range map[string]string{"answer_regex": q.Regex, "answer_not_regex": q.NotRegex} {
		if _, err := regexp.Compile("(?i)" + re); re != "" && err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// Score checks an answer against the question's matchers and returns
// the failures, none when it passes.
func (q Question) Score(answer string) []string {
	a := strings.ToLower(answer)
	var fails []string
	for _, s := range q.Contains {
		if !strings.Contains(a, strings.ToLower(s)) {
			fails = append(fails, fmt.Sprintf("missing %q", s))
		}
	}
	if i := missingInOrder(a, q.Sequence); i == 0 {
		fails = append(fails, fmt.Sprintf("sequence: missing %q", q.Sequence[0]))
	} else if i > 0 {
		fails = append(fails, fmt.Sprintf("sequence: %q not after %q", q.Sequence[i],
			strings.Join(q.Sequence[:i], ", ")))
	}
	for _, s := range q.Excludes {
		if q.excluded(a, strings.ToLower(s)) {
			fails = append(fails, fmt.Sprintf("contains excluded %q", s))
		}
	}
	if q.Regex != "" && !regexp.MustCompile("(?i)"+q.Regex).MatchString(answer) {
		fails = append(fails, fmt.Sprintf("no match for /%s/", q.Regex))
	}
	if q.NotRegex != "" && regexp.MustCompile("(?i)"+q.NotRegex).MatchString(answer) {
		fails = append(fails, fmt.Sprintf("matches excluded /%s/", q.NotRegex))
	}
	return fails
}

// clauseEnd splits an answer into the clauses ExcludesUnless scopes:
// at ";", a line end, or a "." that ends a sentence (followed by space
// or the end), so the dots of a file name or an address do not split.
// An abbreviation's "." before a space ("e.g. x") splits as well: an
// ExcludesUnless word then counts only on its own side of it.
var clauseEnd = regexp.MustCompile(`;|\n|\.(\s|$)`)

// excluded reports whether the lower-cased answer a holds the
// lower-cased excluded string s: anywhere, or with ExcludesUnless set,
// in a clause that holds none of its words. The words match whole
// words, so "plain" does not count inside "explain".
func (q Question) excluded(a, s string) bool {
	if len(q.ExcludesUnless) == 0 {
		return strings.Contains(a, s)
	}
	words := make([]string, len(q.ExcludesUnless))
	for i, w := range q.ExcludesUnless {
		words[i] = regexp.QuoteMeta(strings.ToLower(w))
	}
	unless := regexp.MustCompile(`\b(?:` + strings.Join(words, "|") + `)\b`)
	for _, clause := range clauseEnd.Split(a, -1) {
		if strings.Contains(clause, s) && !unless.MatchString(clause) {
			return true
		}
	}
	return false
}

// missingInOrder is the index of the first item of seq that does not
// occur in a after the previous item's occurrence, or -1 when all do.
func missingInOrder(a string, seq []string) int {
	at := 0
	for i, s := range seq {
		ls := strings.ToLower(s)
		j := strings.Index(a[at:], ls)
		if j < 0 {
			return i
		}
		at += j + len(ls)
	}
	return -1
}

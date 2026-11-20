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
// pass threshold pinned with the questions (PLAN.md M5: "pinned when the
// question set is written"), and the questions.
type Set struct {
	Scenario  string     `yaml:"scenario"`
	Threshold float64    `yaml:"threshold"`
	Questions []Question `yaml:"questions"`
}

// Question is one question and the matchers its answer is scored by.
// Every matcher set must hold; all compare case-insensitively.
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
// matcher, a duplicate id, an empty matcher string or a bad regex.
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
	}
	for _, list := range [][]string{q.Contains, q.Sequence, q.Excludes} {
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
		if strings.Contains(a, strings.ToLower(s)) {
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

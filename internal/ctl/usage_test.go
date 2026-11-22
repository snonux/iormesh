package ctl

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

// helpInvocations are the command lines `SUBCMD -h` is tried on: every
// operator subcommand, each services verb and show view, routes'
// discover.
func helpInvocations() [][]string {
	var out [][]string
	for _, name := range []string{"add", "rm", "collect", "deploy", "build", "refresh", "open", "tui",
		"expand", "query", "verify", "status", "list", "ensure"} {
		out = append(out, []string{name})
	}
	for _, v := range showViews {
		out = append(out, []string{"show", v})
	}
	for _, v := range []string{"list", "edges", "name", "rules", "unname"} {
		out = append(out, []string{"services", v})
	}
	return append(out, []string{"routes", "discover"}, []string{"routes"}, []string{"show"},
		[]string{"services"})
}

// TestSubcommandHelpExitsZero pins task vh4: `SUBCMD -h` (and --help)
// is a request, not an error. It exits 0 and prints only that
// subcommand's flags — no "flag: help requested" line and no full
// usage after them.
func TestSubcommandHelpExitsZero(t *testing.T) {
	for _, inv := range helpInvocations() {
		for _, h := range []string{"-h", "--help"} {
			e, stdout, stderr, _ := testEnv(t)
			args := append(append([]string{}, inv...), h)
			code := run(args, e)
			out := stdout.String() + stderr.String()
			if code != exitOK {
				t.Errorf("%v: exit %d, want 0; output %q", args, code, out)
			}
			if strings.Contains(out, "help requested") || strings.Contains(out, "usage:\n") {
				t.Errorf("%v: printed more than the subcommand's help: %q", args, out)
			}
			if !strings.Contains(stderr.String(), "Usage of "+strings.Join(inv, " ")) {
				t.Errorf("%v: no subcommand help on stderr: %q", args, stderr)
			}
		}
	}
}

// TestSubcommandBadFlagStillUsage is the negative side: an unknown
// flag stays a usage error, exit 2 with the full usage.
func TestSubcommandBadFlagStillUsage(t *testing.T) {
	e, _, stderr, _ := testEnv(t)
	if code := run([]string{"show", "-nope"}, e); code != exitUsage ||
		!strings.Contains(stderr.String(), "usage:\n") {
		t.Errorf("show -nope: exit %d, stderr %q", code, stderr)
	}
}

// flagsOf runs `inv -h` and returns the flag names its help lists.
func flagsOf(t *testing.T, inv []string) []string {
	t.Helper()
	e, _, stderr, _ := testEnv(t)
	run(append(append([]string{}, inv...), "-h"), e)
	var names []string
	for _, line := range strings.Split(stderr.String(), "\n") {
		rest, ok := strings.CutPrefix(line, "  -")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(rest, " ")
		name, _, _ = strings.Cut(name, "\t")
		names = append(names, name)
	}
	return names
}

// usageEntry is the usage text's entry (its first line and the
// continuation lines under it) for `iormeshctl name verb`; verb may be
// empty. A services verb matches as "verb" or "[verb]" (list is the
// default verb).
func usageEntry(text, name, verb string) string {
	var b strings.Builder
	in := false
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(line, "  iormeshctl "); ok {
			fields := strings.Fields(rest)
			in = len(fields) > 0 && fields[0] == name
			if in && verb != "" && name != "show" {
				in = len(fields) > 1 && (fields[1] == verb || fields[1] == "["+verb+"]")
			}
		} else if !strings.HasPrefix(line, "    ") {
			in = false
		}
		if in {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// TestUsageNamesEverySubcommandAndFlag keeps the usage text from
// drifting (task vh4): every registered subcommand, services verb and
// show view has an entry, and the entry names every flag the
// subcommand's flag set registers. [K8S] stands for -context and
// -namespace, as the usage text explains.
func TestUsageNamesEverySubcommandAndFlag(t *testing.T) {
	var buf bytes.Buffer
	usage(&buf)
	text := buf.String()
	for name := range commands {
		if name == checkCommand {
			continue // build's child, not for operators
		}
		verbs := []string{""}
		switch name {
		case "services":
			verbs = nil
			for v := range servicesVerbs {
				verbs = append(verbs, v)
			}
		case "routes":
			verbs = []string{"discover"}
		case "show":
			verbs = showViews
		}
		for _, verb := range verbs {
			checkUsageEntry(t, usageEntry(text, name, verb), name, verb)
		}
	}
}

// namesWord reports whether text holds word whole: not inside a longer
// name, so -type is not satisfied by -selinux-type, nor a view thread
// by threads.
func namesWord(text, word string) bool {
	return regexp.MustCompile(`(^|[^A-Za-z0-9-])` + regexp.QuoteMeta(word) + `($|[^A-Za-z0-9-])`).
		MatchString(text)
}

// checkUsageEntry holds one usage entry to `name verb -h`: the entry
// exists, names show's view, and names every flag the help lists —
// at least one, so a help the parser stopped producing cannot pass
// for a subcommand without flags (every iormeshctl subcommand has
// some).
func checkUsageEntry(t *testing.T, entry, name, verb string) {
	t.Helper()
	if entry == "" {
		t.Errorf("usage has no entry for %s %s", name, verb)
		return
	}
	if name == "show" && !namesWord(entry, verb) {
		t.Errorf("show's usage does not name the view %s:\n%s", verb, entry)
	}
	inv := []string{name}
	if verb != "" {
		inv = append(inv, verb)
	}
	flags := flagsOf(t, inv)
	if len(flags) == 0 {
		t.Errorf("%s %s -h listed no flags", name, verb)
	}
	for _, f := range flags {
		if (f == "context" || f == "namespace") && strings.Contains(entry, "[K8S]") {
			continue
		}
		if !namesWord(entry, "-"+f) {
			t.Errorf("usage of %s %s does not name -%s:\n%s", name, verb, f, entry)
		}
	}
}

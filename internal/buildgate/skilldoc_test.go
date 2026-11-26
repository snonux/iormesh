package buildgate

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/ctl"
)

// The iormesh agent skill (decision #37, docs/milestones/m8.md, task om4)
// is read by a smaller model that runs, or tells its user to type, the
// commands it names exactly as written. A renamed subcommand or a moved
// guide chapter would not fail any build; it would send that model down a
// dead end. This gate is the compiler for the skill: every iormeshctl and
// iormesh subcommand, and every flag after it, that the skill names must
// be in the programs' own usage output,
// every relative link must resolve, and the frontmatter must carry what
// both Claude Code and pi load a skill by.

// skillDir is the skill, relative to this package.
const skillDir = "../../docs/skills/iormesh"

// guideDir is the guide the skill points into for the path.
const guideDir = "../../docs/guide"

// commandSet is what a program's usage output allows: its subcommands,
// the second word each multi-verb subcommand takes (show's views,
// services' and routes' verbs), and the flags of each.
type commandSet struct {
	top  map[string]bool
	verb map[string]map[string]bool
	// synFlags are the flags each subcommand's synopsis lines name.
	synFlags map[string]map[string]bool
	// flags returns the flags "sub" or "sub verb" accepts.
	flags func(key string) map[string]bool
}

// usageWord is a subcommand or verb in a synopsis line; brackets mark an
// optional verb (`services [list]`).
var usageWord = regexp.MustCompile(`^\[?([a-z][a-z0-9-]*)\]?$`)

// usageFlag is a flag named in a synopsis line: after a space, a bracket,
// a parenthesis or a "|" ("(-pid PID | -pattern GLOB", "[-aggregate=false]").
var usageFlag = regexp.MustCompile(`(?:^|[\s\[(|])-([a-z][a-z0-9-]*)`)

// parseUsage reads the synopsis lines of a usage text: each is indented
// two spaces and starts with prog, and the lines indented deeper below it
// continue it. The words after a "VIEW:" line, split on "|", are show's
// views. Prose lines are not indented and are skipped. Its flags are the
// synopsis flags; realCommands replaces them for iormeshctl, whose
// synopsis folds the Kubernetes flags into a [K8S] placeholder.
func parseUsage(prog, text string) commandSet {
	cs := commandSet{top: map[string]bool{}, verb: map[string]map[string]bool{},
		synFlags: map[string]map[string]bool{}}
	cs.flags = func(key string) map[string]bool {
		sub, _, _ := strings.Cut(key, " ")
		return cs.synFlags[sub]
	}
	lines := strings.Split(text, "\n")
	cur := ""
	for i, line := range lines {
		fields := strings.Fields(line)
		switch {
		case strings.HasPrefix(line, "  "+prog+" ") && len(fields) > 1:
			cur = fields[1]
			cs.top[cur] = true
			if len(fields) > 2 {
				if m := usageWord.FindStringSubmatch(fields[2]); m != nil {
					cs.addVerb(cur, m[1])
				}
			}
		case !strings.HasPrefix(line, "   "):
			cur = ""
		}
		if cur != "" {
			cs.addFlags(cur, line)
		}
		if _, views, ok := strings.Cut(line, "VIEW:"); ok {
			for j := i + 1; j < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[j]), "|"); j++ {
				views += " " + lines[j]
			}
			for _, v := range strings.Split(views, "|") {
				if f := strings.Fields(v); len(f) > 0 {
					cs.addVerb("show", f[0])
				}
			}
		}
	}
	return cs
}

// addFlags records the flags a synopsis line of sub names. The tracer's
// `start [run's flags]` takes every run flag, so it gets run's.
func (cs commandSet) addFlags(sub, line string) {
	if cs.synFlags[sub] == nil {
		cs.synFlags[sub] = map[string]bool{}
	}
	for _, m := range usageFlag.FindAllStringSubmatch(line, -1) {
		cs.synFlags[sub][m[1]] = true
	}
	if strings.Contains(line, "[run's flags]") {
		for f := range cs.synFlags["run"] {
			cs.synFlags[sub][f] = true
		}
	}
}

func (cs commandSet) addVerb(cmd, verb string) {
	if cs.verb[cmd] == nil {
		cs.verb[cmd] = map[string]bool{}
	}
	cs.verb[cmd][verb] = true
}

// tracerUsageFile holds the tracer's usage function.
const tracerUsageFile = "../cli/cli.go"

// tracerUsage returns the text the tracer's `iormesh help` prints: the
// raw string literal in internal/cli's usage function, read from the
// source. Importing internal/cli to run it would make this package need
// the built BPF object (internal/bpfembed.go embeds it), which a gate on
// docs has no use for; usage prints the literal as it is, so the source
// is the output.
func tracerUsage(t *testing.T) string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), tracerUsageFile, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", tracerUsageFile, err)
	}
	var text string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "usage" {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && strings.HasPrefix(lit.Value, "`") {
				text = strings.Trim(lit.Value, "`")
			}
			return true
		})
	}
	if text == "" {
		t.Fatalf("%s: no usage function printing a raw string literal", tracerUsageFile)
	}
	return text
}

// realCommands parses both programs' usage: iormeshctl's from running its
// help, the tracer's from tracerUsage.
func realCommands(t *testing.T) map[string]commandSet {
	t.Helper()
	var ctlOut, sink bytes.Buffer
	if code := ctl.Main([]string{"help"}, &ctlOut, &sink); code != 0 {
		t.Fatalf("iormeshctl help: exit %d: %s", code, sink.String())
	}
	sets := map[string]commandSet{
		"iormeshctl": parseUsage("iormeshctl", ctlOut.String()),
		"iormesh":    parseUsage("iormesh", tracerUsage(t)),
	}
	ctlSet, runFlags := sets["iormeshctl"], sets["iormesh"].synFlags["run"]
	ctlSet.flags = func(key string) map[string]bool {
		flags := ctlFlags(strings.Fields(key))
		if key == "add" { // the flags after HOST are the tracer's run flags
			for f := range runFlags {
				flags[f] = true
			}
		}
		return flags
	}
	sets["iormeshctl"] = ctlSet
	if len(runFlags) < 5 || len(ctlSet.flags("deploy")) < 3 {
		t.Fatalf("parsed run flags %v, deploy flags %v; did the help layout change?", runFlags, ctlSet.flags("deploy"))
	}
	for prog, cs := range sets {
		if len(cs.top) < 5 {
			t.Fatalf("%s help: parsed only %v; did the usage layout change?", prog, cs.top)
		}
	}
	if len(sets["iormeshctl"].verb["show"]) < 5 {
		t.Fatalf("iormeshctl help: parsed show views %v; did the VIEW line change?", sets["iormeshctl"].verb["show"])
	}
	return sets
}

// ctlFlags returns the flags `iormeshctl INV... -h` lists: its flag set's
// help, one "  -name ..." line per flag on stderr.
func ctlFlags(inv []string) map[string]bool {
	var sink, help bytes.Buffer
	ctl.Main(append(append([]string{}, inv...), "-h"), &sink, &help)
	flags := map[string]bool{}
	for _, line := range strings.Split(help.String(), "\n") {
		if rest, ok := strings.CutPrefix(line, "  -"); ok {
			name, _, _ := strings.Cut(rest, " ")
			name, _, _ = strings.Cut(name, "\t")
			flags[name] = true
		}
	}
	return flags
}

// codeSpan is a fenced block's body or an inline code span.
var codeSpan = regexp.MustCompile("(?s)```[^\n]*\n(.*?)```|`([^`\n]+)`")

// codeLines returns the lines of every code span in a markdown text.
func codeLines(text string) []string {
	var out []string
	for _, m := range codeSpan.FindAllStringSubmatch(text, -1) {
		out = append(out, strings.Split(m[1]+m[2], "\n")...)
	}
	return out
}

// releaseCtl is a release archive's controller binary, run before it is
// installed as iormeshctl (guide chapter 3).
var releaseCtl = regexp.MustCompile(`^iormeshctl-(linux|darwin)-(amd64|arm64)$`)

// progOf names the program a shell token runs: iormeshctl (or a release
// archive's copy), iormesh (bare, or the installed /opt/iormesh/bin/iormesh),
// or "" for anything else — a path such as docs/skills/iormesh is no
// program.
func progOf(tok string) string {
	tok = strings.TrimPrefix(tok, "./")
	base := tok
	if strings.Contains(tok, "/") {
		if !strings.HasSuffix(tok, "/bin/iormesh") && !strings.HasSuffix(tok, "/bin/iormeshctl") {
			return ""
		}
		base = tok[strings.LastIndex(tok, "/")+1:]
	}
	switch {
	case base == "iormeshctl", releaseCtl.MatchString(base):
		return "iormeshctl"
	case base == "iormesh":
		return "iormesh"
	}
	return ""
}

// endOfCommand is a token after which no subcommand follows on the line.
func endOfCommand(tok string) bool {
	return tok == "" || strings.HasPrefix(tok, "-") || strings.HasPrefix(tok, "#") ||
		strings.ContainsAny(tok[:1], "|;&)>")
}

// cmdFlag is a flag token in a documented command line ("-dist",
// "-aggregate=false", "--help").
var cmdFlag = regexp.MustCompile(`^--?([a-z][a-z0-9-]*)(=.*)?$`)

// staleCommand checks one command line, args starting at the subcommand:
// the subcommand, its verb when it takes one, and each flag up to the end
// of the shell command. It returns what usage does not list.
func staleCommand(prog string, args []string, cs commandSet) []string {
	sub, key, rest := args[0], args[0], args[1:]
	if !cs.top[sub] {
		return []string{prog + " " + sub}
	}
	if verbs := cs.verb[sub]; verbs != nil && len(rest) > 0 && usageWord.MatchString(rest[0]) &&
		!endOfCommand(rest[0]) {
		if !verbs[rest[0]] {
			return []string{prog + " " + sub + " " + rest[0]}
		}
		key, rest = sub+" "+rest[0], rest[1:]
	}
	var stale []string
	flags := cs.flags(key)
	for _, tok := range rest {
		if tok == "#" || strings.HasPrefix(tok, "#") || strings.ContainsAny(tok[:1], "|;&)>") {
			break
		}
		m := cmdFlag.FindStringSubmatch(tok)
		if m != nil && m[1] != "h" && m[1] != "help" && !flags[m[1]] {
			stale = append(stale, prog+" "+key+" -"+m[1])
		}
	}
	return stale
}

// staleCommands returns each command in a markdown text that its
// program's usage does not list, as "prog sub", "prog sub verb" or
// "prog sub -flag".
func staleCommands(text string, sets map[string]commandSet) []string {
	var stale []string
	for _, line := range codeLines(text) {
		// Quotes and punctuation around a command in an example's prose
		// ("... ends in "sudo -n ... iormesh status".") are not part of it.
		// A colon is kept: "iormesh:" opens a message, not a command. A
		// leading dot is kept for "./iormeshctl-linux-amd64".
		toks := strings.Fields(line)
		for i := range toks {
			toks[i] = strings.TrimLeft(strings.TrimRight(toks[i], `"'.,`), `"',`)
			if toks[i] == "" {
				toks[i] = "#"
			}
		}
		for i, tok := range toks {
			if prog := progOf(tok); prog != "" && i+1 < len(toks) && !endOfCommand(toks[i+1]) {
				stale = append(stale, staleCommand(prog, toks[i+1:], sets[prog])...)
			}
		}
	}
	return stale
}

// skillFiles returns every markdown file of the skill, by path.
func skillFiles(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(skillDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".md" {
			return err
		}
		b, err := os.ReadFile(path)
		files[path] = string(b)
		return err
	})
	if err != nil {
		t.Fatalf("read the skill: %v", err)
	}
	if _, ok := files[filepath.Join(skillDir, "SKILL.md")]; !ok {
		t.Fatalf("%s/SKILL.md is missing", skillDir)
	}
	return files
}

// TestSkillNamesOnlyRealCommands fails on any iormeshctl or iormesh
// subcommand (and show/services/routes verb) the skill names in code that
// the programs' usage does not list.
func TestSkillNamesOnlyRealCommands(t *testing.T) {
	sets := realCommands(t)
	for path, text := range skillFiles(t) {
		for _, c := range staleCommands(text, sets) {
			t.Errorf("%s: `%s` is not in the program's usage output", path, c)
		}
	}
}

// TestSkillCommandGateCatchesMadeUpCommands is the gate's negative side:
// a made-up subcommand, show view or tracer subcommand is reported, the
// real ones and non-program paths are not.
func TestSkillCommandGateCatchesMadeUpCommands(t *testing.T) {
	sets := realCommands(t)
	text := "Run `iormeshctl frobnicate app1`, then\n```sh\niormeshctl show bogus -json\n" +
		"sudo -n /opt/iormesh/bin/iormesh launch\nSuccess: ends in \"iormesh bogus\".\n" +
		"iormeshctl deploy -bogus app1   # a made-up flag\niormesh stop -project p -nope\n```\n"
	want := []string{"iormeshctl frobnicate", "iormeshctl show bogus", "iormesh launch", "iormesh bogus",
		"iormeshctl deploy -bogus", "iormesh stop -nope"}
	if got := staleCommands(text, sets); !slices.Equal(got, want) {
		t.Errorf("made-up commands: got %q, want %q", got, want)
	}
	good := "`iormeshctl show edges -unresolved`, `iormeshctl services name -port 1 'h*' s`,\n" +
		"`./iormeshctl-linux-amd64 sign -key K .`, `sudo /opt/iormesh/bin/iormesh stop -project p`,\n" +
		"`pi --skill docs/skills/iormesh`, `iormeshctl -h`, `iormeshctl show sequence 0002`\n" +
		"```text\nSuccess: ends in \"sudo -n /opt/iormesh/bin/iormesh status\".\n```\n" +
		"`iormeshctl deploy -dist D app1 | tee x -q`, `iormeshctl add -project p -pattern 'j*' app1 -duration 8h`,\n" +
		"`iormesh start -pattern x -project p -cpu-quota 50%`, `iormeshctl services name -port 1 'h*' s`\n"
	if got := staleCommands(good, sets); len(got) != 0 {
		t.Errorf("real commands reported as stale: %q", got)
	}
}

// mdLink is a markdown link's target.
var mdLink = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// headingAnchors returns the GitHub-style anchors of a markdown file's
// headings outside fenced blocks, a repeated one numbered as GitHub does.
func headingAnchors(text string) map[string]bool {
	out, seen, fence := map[string]bool{}, map[string]int{}, false
	strip := regexp.MustCompile(`[^\w\- ]`)
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "```") {
			fence = !fence
		}
		h, ok := strings.CutPrefix(strings.TrimLeft(line, "#"), " ")
		if fence || !ok || !strings.HasPrefix(line, "#") {
			continue
		}
		s := strings.ReplaceAll(strip.ReplaceAllString(strings.ToLower(strings.TrimSpace(h)), ""), " ", "-")
		if n := seen[s]; n > 0 {
			out[fmt.Sprintf("%s-%d", s, n)] = true
		} else {
			out[s] = true
		}
		seen[s]++
	}
	return out
}

// brokenLinks returns the relative links of the markdown file at path
// whose file, or #anchor in a .md file, does not exist, and counts the
// links that land in the guide.
func brokenLinks(path, text string) (broken []string, intoGuide int) {
	guide, _ := filepath.Abs(guideDir)
	for _, m := range mdLink.FindAllStringSubmatch(codeSpan.ReplaceAllString(text, ""), -1) {
		target, frag, _ := strings.Cut(m[1], "#")
		if regexp.MustCompile(`^[a-z]+:`).MatchString(target) {
			continue
		}
		file := path
		if target != "" {
			file = filepath.Join(filepath.Dir(path), target)
		}
		b, err := os.ReadFile(file)
		if err != nil {
			broken = append(broken, m[1]+": "+err.Error())
			continue
		}
		if frag != "" && filepath.Ext(file) == ".md" && !headingAnchors(string(b))[frag] {
			broken = append(broken, m[1]+": no such heading")
			continue
		}
		if abs, _ := filepath.Abs(file); strings.HasPrefix(abs, guide+string(filepath.Separator)) {
			intoGuide++
		}
	}
	return broken, intoGuide
}

// TestSkillLinksResolve holds every relative link of the skill to a file
// (and heading) that exists, and the skill to pointing into the guide,
// which keeps the path (one home per fact, AGENTS.md).
func TestSkillLinksResolve(t *testing.T) {
	total := 0
	for path, text := range skillFiles(t) {
		broken, n := brokenLinks(path, text)
		for _, b := range broken {
			t.Errorf("%s: broken link %s", path, b)
		}
		total += n
	}
	if total == 0 {
		t.Error("the skill links nowhere into docs/guide; it must point there for the path")
	}
}

// TestSkillLinkGateCatchesBrokenLinks is the link gate's negative side.
func TestSkillLinkGateCatchesBrokenLinks(t *testing.T) {
	path := filepath.Join(skillDir, "SKILL.md")
	text := "[a](../../guide/99-none.md) [b](../../guide/README.md#no-such-heading) " +
		"[c](../../guide/README.md#chapters) [d](https://example.org/x)"
	broken, n := brokenLinks(path, text)
	if len(broken) != 2 || n != 1 {
		t.Errorf("got broken %q and %d guide links, want 2 broken and 1", broken, n)
	}
}

// TestSkillFrontmatter holds SKILL.md's frontmatter to the keys both
// Claude Code and pi accept: a name matching the folder and a quoted,
// non-empty description within the spec's 1024 characters. pi skips a
// skill whose description is missing or not valid YAML.
func TestSkillFrontmatter(t *testing.T) {
	text := skillFiles(t)[filepath.Join(skillDir, "SKILL.md")]
	front, ok := strings.CutPrefix(text, "---\n")
	front, _, closed := strings.Cut(front, "\n---\n")
	if !ok || !closed {
		t.Fatal("SKILL.md does not start with a --- frontmatter block")
	}
	keys := map[string]string{}
	for _, line := range strings.Split(front, "\n") {
		k, v, found := strings.Cut(line, ":")
		if !found {
			t.Errorf("frontmatter line %q is not key: value", line)
			continue
		}
		keys[k] = strings.TrimSpace(v)
	}
	for k := range keys {
		if k != "name" && k != "description" {
			t.Errorf("frontmatter key %q: keep to name and description, which both agents accept", k)
		}
	}
	if keys["name"] != filepath.Base(skillDir) {
		t.Errorf("name %q, want %q (the folder's name)", keys["name"], filepath.Base(skillDir))
	}
	d := keys["description"]
	if len(d) < 3 || d[0] != '"' || d[len(d)-1] != '"' || len(d)-2 > 1024 {
		t.Errorf("description must be double-quoted, non-empty, at most 1024 characters: %q", d)
	}
}

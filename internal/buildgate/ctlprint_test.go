package buildgate

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// ctlPkg is the controller's command package: everything iormeshctl
// prints to the operator's terminal is printed there.
const ctlPkg = "iormesh/internal/ctl"

// printFuncs are the fmt functions TestCtlPrintsAreTerminalSafe checks,
// each with the index of its first value argument; the f-variants'
// format sits just before it.
var printFuncs = map[string]int{
	"Fprint": 1, "Fprintln": 1, "Fprintf": 2,
	"Print": 0, "Println": 0, "Printf": 1,
}

// sanitizers are the functions whose result may be printed as it is:
// termsafe's escapers and internal/ctl's two names for termsafe.String
// (safe, and errText for an error's text). A helper that builds text
// from safe parts is not listed: its result is wrapped once more at the
// print, which costs nothing, because termsafe.String leaves printable
// text, its own output included, unchanged.
var sanitizers = map[string]bool{
	"iormesh/internal/termsafe.String": true,
	"iormesh/internal/termsafe.JSON":   true,
	ctlPkg + ".safe":                   true,
	ctlPkg + ".errText":                true,
}

// rawAllowed are the reviewed exceptions, "file.go: expression" to why
// the value cannot carry text a terminal acts on. Keep it short: an
// entry is a print the gate no longer watches.
var rawAllowed = map[string]string{
	"routes.go: b.String()": "a strings.Builder filled only by Fprintf calls this gate checks; " +
		"wrapped whole, its newlines would quote every line",
}

// TestCtlPrintsAreTerminalSafe (task oj4) type-checks internal/ctl and
// fails on any fmt print whose value is text not passed through a
// sanitizer: a string, an error, anything with a String, Error or Format
// method. Constants and plain numbers and booleans pass. Hostnames, pod
// names, contexts, paths and service names come from servers, clusters
// and files that may be compromised; after tasks di4, 6i4 and lj4 fixed
// such prints one by one and more were found raw, each print no longer
// chooses whether to escape: this gate makes the choice for all of
// them. Escaping per value rather than per output line keeps iormeshctl's
// own words unquoted next to a quoted hostile value, and leaves the
// JSON that show and query print (termsafe.JSON) as it is.
//
// What it cannot see: text written other than by fmt's print functions
// (io.WriteString, a Write method, a tabwriter fed by such) and text a
// called helper prints to a writer it is handed; internal/ctl prints
// through fmt alone today.
func TestCtlPrintsAreTerminalSafe(t *testing.T) {
	root := repoRoot(t)
	imp := exportImporter(t, root, ctlPkg)
	fset := token.NewFileSet()
	files := parseCtl(t, fset, root)
	info := typeCheck(t, fset, files, imp, ctlPkg)
	got, used := rawPrints(fset, files, info)
	for _, f := range got {
		t.Error(f)
	}
	// A stale exception fails too, which also proves the gate saw prints.
	for key := range rawAllowed {
		if !used[key] {
			t.Errorf("rawAllowed entry %q matches no print; drop it", key)
		}
	}
}

// TestCtlPrintGateCatchesRawText (negative): the gate flags a planted
// raw string, error, Stringer number, Duration, non-constant format and
// spread argument list, and passes constants, plain numbers and
// sanitized text.
func TestCtlPrintGateCatchesRawText(t *testing.T) {
	const src = `package ctl

import (
	"fmt"
	"io"
	"time"

	"iormesh/internal/termsafe"
)

type level int

func (l level) String() string { return "x" }

func safe(s string) string { return termsafe.String(s) }

func errText(err error) string { return termsafe.String(err.Error()) }

func f(w io.Writer, host string, err error, d time.Duration, args []any) {
	fmt.Fprintf(w, "%s %d %t %s %s %s\n", "lit", 3, true, safe(host), termsafe.String(host), errText(err))
	fmt.Fprintf(w, "%s\n", host)
	fmt.Fprintln(w, err)
	fmt.Fprint(w, level(1), d)
	fmt.Printf(host)
	fmt.Println(args...)
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	imp := exportImporter(t, repoRoot(t), ctlPkg)
	info := typeCheck(t, fset, []*ast.File{file}, imp, ctlPkg)
	got, _ := rawPrints(fset, []*ast.File{file}, info)
	want := []string{
		"fixture.go:21: fmt.Fprintf prints host (string) raw",
		"fixture.go:22: fmt.Fprintln prints err (error) raw",
		"fixture.go:23: fmt.Fprint prints level(1) (iormesh/internal/ctl.level) raw",
		"fixture.go:23: fmt.Fprint prints d (time.Duration) raw",
		"fixture.go:24: fmt.Printf has a format that is not a constant",
		"fixture.go:25: fmt.Println spreads args...; list the values",
	}
	if !slices.Equal(got, want) {
		t.Errorf("findings\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// exportImporter imports pkg's dependencies from the export data the go
// command writes for them (go list -export), so the check needs neither
// golang.org/x/tools nor a type-check of every dependency from source.
func exportImporter(t *testing.T, root, pkg string) types.Importer {
	t.Helper()
	out := goList(t, root, "-export", "-deps", "-f", "{{.ImportPath}}\t{{.Export}}", pkg)
	exports := map[string]string{}
	for line := range strings.Lines(out) {
		path, file, _ := strings.Cut(strings.TrimSuffix(line, "\n"), "\t")
		exports[path] = file
	}
	lookup := func(path string) (io.ReadCloser, error) {
		file := exports[path]
		if file == "" {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(file)
	}
	return importer.ForCompiler(token.NewFileSet(), "gc", lookup)
}

// goList runs go list with args in root, CGO_ENABLED=0 as the cgo-free
// controller is built, and returns its stdout; a failure fails t with
// go list's stderr.
func goList(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("go", append([]string{"list"}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return string(out)
}

// parseCtl parses internal/ctl's non-test Go files, as go list names
// them for this platform.
func parseCtl(t *testing.T, fset *token.FileSet, root string) []*ast.File {
	t.Helper()
	out := goList(t, root, "-f", "{{.Dir}}\t{{join .GoFiles \" \"}}", ctlPkg)
	dir, names, _ := strings.Cut(strings.TrimSpace(out), "\t")
	var files []*ast.File
	for _, name := range strings.Fields(names) {
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatalf("go list named no Go file in %s", ctlPkg)
	}
	return files
}

// typeCheck type-checks files as package path and returns what the
// gate reads: each expression's type and constant value, and each
// identifier's object.
func typeCheck(t *testing.T, fset *token.FileSet, files []*ast.File, imp types.Importer,
	path string) *types.Info {
	t.Helper()
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: imp}
	if _, err := conf.Check(path, fset, files, info); err != nil {
		t.Fatalf("type-check %s: %v", path, err)
	}
	return info
}

// rawPrints lists, as "file.go:line: message", every fmt print in files
// that could put unescaped text on the terminal, and which rawAllowed
// entries matched a print.
func rawPrints(fset *token.FileSet, files []*ast.File, info *types.Info) ([]string, map[string]bool) {
	var got []string
	used := map[string]bool{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, first, ok := fmtPrint(info, call)
			if !ok {
				return true
			}
			pos := fset.Position(call.Pos())
			at := fmt.Sprintf("%s:%d: fmt.%s", filepath.Base(pos.Filename), pos.Line, name)
			got = append(got, callFindings(at, name, first, call, info, used)...)
			return true
		})
	}
	return got, used
}

// callFindings checks one fmt print call: a constant format, no spread
// argument list, and each value constant, a plain number or sanitized.
func callFindings(at, name string, first int, call *ast.CallExpr, info *types.Info,
	used map[string]bool) []string {
	var got []string
	if strings.HasSuffix(name, "f") && first > 0 && info.Types[call.Args[first-1]].Value == nil {
		got = append(got, at+" has a format that is not a constant")
	}
	if call.Ellipsis.IsValid() {
		return append(got, fmt.Sprintf("%s spreads %s...; list the values", at,
			types.ExprString(call.Args[len(call.Args)-1])))
	}
	file, _, _ := strings.Cut(at, ":")
	for _, arg := range call.Args[min(first, len(call.Args)):] {
		if printSafe(info, arg) {
			continue
		}
		key := file + ": " + types.ExprString(arg)
		if _, ok := rawAllowed[key]; ok {
			used[key] = true
			continue
		}
		got = append(got, fmt.Sprintf("%s prints %s (%s) raw", at, types.ExprString(arg),
			info.Types[arg].Type))
	}
	return got
}

// fmtPrint reports whether call is one of printFuncs, with its name and
// first value argument's index.
func fmtPrint(info *types.Info, call *ast.CallExpr) (string, int, bool) {
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return "", 0, false
	}
	fn, ok := info.Uses[sel.Sel].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != "fmt" {
		return "", 0, false
	}
	first, ok := printFuncs[fn.Name()]
	return fn.Name(), first, ok
}

// printSafe reports whether arg may be printed as it is: a sanitizer's
// result, or a value fmt prints as written — a constant, or a number or
// boolean (digits, true/false) — without a String, Error or Format
// method to print it otherwise (a constant of such a type is no safer
// than its method).
func printSafe(info *types.Info, arg ast.Expr) bool {
	if call, ok := ast.Unparen(arg).(*ast.CallExpr); ok && sanitizers[calleeName(info, call)] {
		return true
	}
	tv := info.Types[arg]
	for _, m := range []string{"String", "Error", "Format"} {
		if obj, _, _ := types.LookupFieldOrMethod(tv.Type, true, nil, m); obj != nil {
			return false
		}
	}
	if tv.Value != nil {
		return true
	}
	b, ok := tv.Type.Underlying().(*types.Basic)
	return ok && b.Info()&(types.IsNumeric|types.IsBoolean) != 0
}

// calleeName is the called function's full name (pkg/path.Name, or
// (T).Name for a method), "" for a call of anything else.
func calleeName(info *types.Info, call *ast.CallExpr) string {
	var id *ast.Ident
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		id = fun
	case *ast.SelectorExpr:
		id = fun.Sel
	default:
		return ""
	}
	fn, ok := info.Uses[id].(*types.Func)
	if !ok {
		return ""
	}
	return fn.FullName()
}

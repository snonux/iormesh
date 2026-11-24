package integrationtests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"iormesh/internal/eval"
)

// The eval criterion (docs/milestones.md M5 "LLM introspection eval") needs a
// real model, and the eval sends the trace's rollup and slices to it, so it
// runs only with the owner's model command — never here. What the rig
// can prove is the pipeline: eval.Main builds the store into its own
// scratch database, reads `show report`, runs every reply's QUERY and
// SOURCE through iormeshctl and the source tree, scores the answers
// with the declared matchers, writes the results and holds the score to
// the threshold. The model is a scripted stand-in — this test binary,
// re-executed with standInFlag — that knows the declared answers, and
// for two questions answers only once the slice or the source file it
// asked for showed the evidence: a pipeline that lost a QUERY's or a
// SOURCE's output fails those two.
const (
	standInFlag     = "-iormesh.eval-stand-in"
	evalQuestions   = "../eval/questions/microservices.yaml"
	evalSource      = "../workload/src"
	standInScripted = "scripted"
	standInWrong    = "wrong"
)

// standIn is the stand-in's script for one question: an action to run
// first and the evidence its output must hold ("" answers at once), and
// the answer.
type standIn struct{ action, evidence, answer string }

// standInScript answers every question of the set as declared.
var standInScript = map[string]standIn{
	"order-file-writer": {"QUERY -method '*.DataStore#save'", "/orders.d/order-42.json.tmp",
		"DataStore#save wrote it, through order-42.json.tmp"},
	"http-call-method": {"SOURCE iormesh/workload/scenarios/HttpFetch.java", "class HttpFetch",
		"HttpFetch#call"},
	"orders-config-reader": {answer: "ServiceConfig#load read orders.conf"},
	"gateway-chain":        {answer: "gateway, orders, inventory, billing"},
	"orders-next-call":     {answer: "POST /inventory/reserve, then POST /billing/charge"},
	"billing-cert-reader":  {answer: "orders read it, with ServiceNode#readCert"},
	"inventory-paths":      {answer: "/inventory/reserve and /inventory/stock/widget"},
	"billing-record":       {answer: "invoice-42.json: written as invoice-42.json.tmp, then renamed"},
	"tls-hops":             {answer: "/orders and /billing/charge"},
	"gateway-cleanup":      {answer: "ServiceNode#retire"},
}

// TestMain lets this binary stand in for the eval's model command:
// `integrationtests.test -iormesh.eval-stand-in=MODE QUESTIONS` reads
// one prompt on stdin and writes one reply.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && strings.HasPrefix(os.Args[1], standInFlag+"=") {
		os.Exit(runStandIn(strings.TrimPrefix(os.Args[1], standInFlag+"="), os.Args[2], os.Stdin, os.Stdout))
	}
	os.Exit(m.Run())
}

// runStandIn is one turn of the stand-in.
func runStandIn(mode, questions string, in io.Reader, out io.Writer) int {
	prompt, err := io.ReadAll(in)
	if err == nil {
		var reply string
		if reply, err = standInReply(mode, questions, string(prompt)); err == nil {
			_, err = fmt.Fprintln(out, reply)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "eval stand-in:", err)
		return 1
	}
	return 0
}

// standInReply finds the prompt's question in the set and replies by
// the script; the wrong mode answers nothing useful.
func standInReply(mode, questions, prompt string) (string, error) {
	if mode == standInWrong {
		return "ANSWER: I could not tell from the trace", nil
	}
	set, err := eval.LoadSet(questions)
	if err != nil {
		return "", err
	}
	head, question, _ := strings.Cut(prompt, "\n== Question ==\n")
	_, acts, asked := strings.Cut(head, "\n== Your actions so far ==\n")
	for _, q := range set.Questions {
		if strings.TrimSpace(question) != strings.TrimSpace(q.Question) {
			continue
		}
		s := standInScript[q.ID]
		switch {
		case s.action == "":
		case !asked:
			return s.action, nil
		case !strings.Contains(acts, s.evidence):
			return "ANSWER: the " + s.action + " output did not show it", nil
		}
		return "ANSWER: " + s.answer, nil
	}
	return "", fmt.Errorf("no question of %s in the prompt", questions)
}

// assertEvalPipeline runs eval.Main over the build's store twice: the
// scripted stand-in passes every question (so the threshold), the wrong
// one none, and Main refuses its score.
func assertEvalPipeline(t *testing.T, b *viewsBuild) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	questions := absPath(t, evalQuestions)
	r, err := runEval(t, b, fmt.Sprintf("%s %s=%s %s", exe, standInFlag, standInScripted, questions))
	if err != nil || !r.Passed || r.Score != 1 {
		t.Errorf("scripted stand-in: score %v, passed %v, err %v: %+v", r.Score, r.Passed, err, r.Questions)
	}
	for _, q := range r.Questions {
		if s := standInScript[q.ID]; s.action != "" && (len(q.Actions) != 1 || q.Actions[0] != s.action) {
			t.Errorf("%s: actions %q, want %q", q.ID, q.Actions, s.action)
		}
	}
	r, err = runEval(t, b, fmt.Sprintf("%s %s=%s %s", exe, standInFlag, standInWrong, questions))
	if err == nil || !strings.Contains(err.Error(), "below the threshold") || r.Score != 0 {
		t.Errorf("wrong stand-in: score %v, err %v", r.Score, err)
	}
}

// runEval runs eval.Main with model and reads the result it wrote.
func runEval(t *testing.T, b *viewsBuild, model string) (eval.Result, error) {
	t.Helper()
	o := eval.Options{Questions: absPath(t, evalQuestions), Store: b.store, Iormeshctl: b.ctl,
		Source: absPath(t, evalSource), Out: t.TempDir(), ModelCmd: model, ModelName: "scripted stand-in",
		Date: "2026-11-20", MaxTurns: eval.DefaultMaxTurns, SliceBytes: eval.DefaultSliceBytes,
		Timeout: time.Minute}
	var stdout bytes.Buffer
	runErr := eval.Main(context.Background(), o, &stdout)
	t.Logf("eval (%s):\n%s", strings.Fields(model)[1], stdout.String())
	data, err := os.ReadFile(filepath.Join(o.Out, o.Date+"-microservices.json"))
	if err != nil {
		t.Fatalf("eval wrote no results: %v (Main: %v)", err, runErr)
	}
	var r eval.Result
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r, runErr
}

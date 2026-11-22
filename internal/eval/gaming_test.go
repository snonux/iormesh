package eval

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// candidates is every string a guesser could list for the set: each
// question's expected and excluded strings, the four services, and every
// path, method and word token of the scenario's declarations.
func candidates(t *testing.T, s Set) []string {
	t.Helper()
	expect, err := os.ReadFile(repoFile("workload/scenarios/" + s.Scenario + "/expect.json"))
	if err != nil {
		t.Fatal(err)
	}
	out := regexp.MustCompile(`[A-Za-z0-9_./#-]{3,}`).FindAllString(string(expect), -1)
	out = append(out, "gateway", "orders", "inventory", "billing", "rename", "renamed")
	for _, q := range s.Questions {
		out = slices.Concat(out, q.Contains, q.Sequence, q.Excludes)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// TestListingEverythingFails: an answer that lists every candidate —
// sorted, and also in the order the declarations name them — passes no
// question, so the threshold cannot be reached by listing instead of
// knowing.
func TestListingEverythingFails(t *testing.T) {
	s, err := LoadSet(repoFile("eval/questions/microservices.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	all := candidates(t, s)
	reversed := slices.Clone(all)
	slices.Reverse(reversed)
	for name, dump := range map[string]string{"sorted": strings.Join(all, "\n"),
		"reversed": strings.Join(reversed, ", ")} {
		for _, q := range s.Questions {
			if len(q.Score(dump)) == 0 {
				t.Errorf("%s: a %s list of every candidate passes", q.ID, name)
			}
		}
	}
}

// TestNonAnswersFail: an empty answer and a refusal pass no question —
// every question needs something positive in the answer — and the
// gateway chain told from the wrong start fails although it holds the
// right sequence after it.
func TestNonAnswersFail(t *testing.T) {
	s, err := LoadSet(repoFile("eval/questions/microservices.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, reply := range []string{"ANSWER:", "ANSWER: I do not know"} {
		act, ok := ParseReply(reply)
		if !ok || act.Kind != ActAnswer {
			t.Fatalf("%q: parsed as %+v, %v", reply, act, ok)
		}
		for _, q := range s.Questions {
			if len(q.Score(act.Arg)) == 0 {
				t.Errorf("%s: %q passes", q.ID, reply)
			}
		}
	}
	for _, q := range s.Questions {
		if q.ID != "gateway-chain" {
			continue
		}
		for _, wrong := range []string{"orders, then gateway, orders, inventory, billing",
			"billing; gateway -> orders -> inventory -> billing"} {
			if len(q.Score(wrong)) == 0 {
				t.Errorf("gateway-chain: the wrong start %q passes", wrong)
			}
		}
		// The right chain passes with a label in front and with the
		// gateway mentioned again in an explanation after it.
		for _, right := range []string{"The chain: gateway, orders, inventory, billing.",
			"1. gateway (the checkout entry point)\n2. orders (called by the gateway)\n3. inventory\n4. billing",
			"gateway -> orders -> inventory -> billing. The gateway's POST /orders starts the checkout.",
			"The gateway calls orders, which calls inventory and then billing.",
			"**The chain:** the gateway, then orders, inventory and billing."} {
			if fails := q.Score(right); len(fails) != 0 {
				t.Errorf("gateway-chain: the right chain %q fails: %v", right, fails)
			}
		}
	}
}

// TestAnswersPass: the declared right answer, written concisely, passes
// each question — the exclusions do not reject the truth.
func TestAnswersPass(t *testing.T) {
	s, err := LoadSet(repoFile("eval/questions/microservices.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	answers := map[string]string{
		"order-file-writer":    "iormesh.workload.scenarios.DataStore#save, through order-42.json.tmp",
		"orders-config-reader": "ServiceConfig#load read orders.conf",
		"gateway-chain":        "gateway -> orders -> inventory -> billing (then inventory again)",
		"orders-next-call":     "POST /inventory/reserve, then POST /billing/charge",
		"http-call-method":     "iormesh.workload.scenarios.HttpFetch#call",
		"billing-cert-reader":  "orders, with ServiceNode#readCert",
		"inventory-paths":      "/inventory/reserve and /inventory/stock/widget",
		"billing-record":       "invoice-42.json: written as invoice-42.json.tmp, then renamed",
		"tls-hops":             "POST /orders and POST /billing/charge",
		"gateway-cleanup":      "ServiceNode#retire",
	}
	for _, q := range s.Questions {
		if fails := q.Score(answers[q.ID]); len(fails) != 0 {
			t.Errorf("%s: the right answer fails: %v", q.ID, fails)
		}
	}
}

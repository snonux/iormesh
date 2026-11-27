package agenttrial

import (
	"path/filepath"
	"testing"

	"iormesh/internal/eval"
)

// glmTLSHops is the tls-hops answer of the bp4 trial's glm guided run
// (runs/20261127T112528Z-ollama-cloud-glm-5.3-flash-cloud-guided,
// transcript turn 35): right, and it names the /inventory/ paths as
// unencrypted. The set's earlier plain answer_excludes failed it (task
// fp4); that run's score stands (#37), the check changed for later runs.
const glmTLSHops = "Two paths went over TLS (https): `POST /orders` " +
	"(gateway→orders) and `POST /billing/charge` (orders→billing) — both " +
	"read `\"scheme\":\"https\"` in the http events (and close via " +
	"`HttpsExchangeImpl`); `POST /inventory/reserve` reads " +
	"`\"scheme\":\"http\"`, and inventory is served `plain` (TLS `plain` in " +
	"`show services`), so `/inventory/stock/widget` and `/inventory/reserve` " +
	"went unencrypted."

// TestTLSHopsPlainInventory: the tls-hops check passes an answer that
// names the /inventory/ hops as plain HTTP and fails one that claims an
// /inventory/ hop went over TLS (owner decision 2026-11-27, task fp4).
func TestTLSHopsPlainInventory(t *testing.T) {
	set, err := eval.LoadSet(filepath.Join("..", "..", "eval", "agent-trial", QuestionsFile))
	if err != nil {
		t.Fatal(err)
	}
	var q eval.Question
	for _, x := range set.Questions {
		if x.ID == "tls-hops" {
			q = x
		}
	}
	if q.ID == "" {
		t.Fatal("no tls-hops question")
	}
	for answer, pass := range map[string]bool{
		glmTLSHops:                    true,
		"/orders and /billing/charge": true,
		"Over TLS: /orders and /billing/charge. The /inventory/ calls were plain HTTP.": true,
		"/orders, /billing/charge and /inventory/reserve went over TLS":                 false,
		"/orders and /billing/charge; /inventory/stock/widget was https too":            false,
		"Over TLS: /orders, /inventory/reserve. /billing/charge was not TLS (plain).":   false,
	} {
		if fails := q.Score(answer); (len(fails) == 0) != pass {
			t.Errorf("%q: failures %v, want pass %v", answer, fails, pass)
		}
	}
}

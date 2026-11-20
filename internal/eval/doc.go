// Package eval is the LLM introspection eval (PLAN.md M5, principle
// #6): a fixed question set over an M0 scenario's trace, with answers
// known from the scenario's declarations, put to a model that works only
// through the per-run summary rollup (`iormeshctl show report`), the
// query interface (`iormeshctl query`) and the workload's source. Its
// score is the evidence behind "precise enough for an LLM to
// reconstruct behavior and check it against source code".
//
// The model is a command, not an API: the operator names it (-model-cmd,
// or IORMESH_EVAL_MODEL_CMD for `mage eval`), the harness writes each
// turn's prompt to its stdin and reads its reply from stdout. Any CLI
// wrapper of a model fits — a local one keeps everything on the
// machine. There is no default: the prompt carries the rollup and the
// query slices, trace data the operator must choose to send, so the
// eval never runs unless a command was named, and `mage test` never
// runs it. Point it at workload traces, never at production ones.
//
// The protocol is plain text, one action per reply:
//
//	QUERY <iormeshctl query flags>   a slice, e.g. QUERY -file '/data/*/order-42.json'
//	SOURCE <path>                    a workload source file, relative to its root
//	ANSWER: <text>                   the final answer
//
// The harness runs the action and appends it and its result to the
// transcript of the next prompt, up to a turn limit. Only the slice
// flags are accepted (never -store or -db), slices are capped, and
// SOURCE reads only below the source root (os.Root), so a reply cannot
// steer the harness elsewhere.
//
// Scoring is deterministic: each question declares matchers (substrings
// that must all appear, a sequence that must appear in order, substrings
// that must not appear, a regular expression), all checked
// case-insensitively; the score is the share of questions passed, and
// the set's pinned threshold decides pass or fail. Results go to
// eval/results/<date>.json.
package eval

# Focused checks and baselines

Part of [Testing strategy](../testing.md).

## Comm renames (task 0g4)

exec.c's `task_rename` program was compiled and loaded but never
attached, so a process renaming itself kept its old `comm` in the
trace and the graph. It is now a control probe (raw tracepoint,
every family), and the correlator applies the main thread's
rename; a worker thread's is ignored (docs/schema.md, `comm`).
`TestRenameReflected` (root, `internal/rename_root_test.go`) runs
a Go child that names a worker thread and then its main thread
with `prctl(PR_SET_NAME)` between two `fstat(-1)` calls. Both
renames must arrive as records, the first call must keep the old
comm and the second carry the new one. correlate's
`TestRenameRefreshesComm` holds the leader/worker rule.
`TestEveryHandWrittenProgramIsAttached` now lists the programs
from the compiled object's ELF symbols rather than a regex over
the C, and requires every attached program to be in the object
(task bg4).


## Command-line help (task vh4)

milestones.md M4 ("Command-line conventions") makes `SUBCMD -h` a
request: exit 0, only that subcommand's flags. Both binaries pin
it with `TestSubcommandHelpExitsZero` (`internal/ctl/usage_test.go`
over every subcommand, services verb and show view;
`internal/cli/cli_test.go` over every tracer subcommand), each with
the negative case that an unknown flag still exits 2 with the full
usage. `TestUsageNamesEverySubcommandAndFlag` runs `-h` on every
entry of `iormeshctl`'s dispatch tables (`commands`,
`servicesVerbs`, `showViews`) and fails when the usage text lacks an
entry, a view or a flag the flag set registers (`[K8S]` stands for
`-context` and `-namespace`). Add a subcommand or a flag and the
test asks for its usage line.


## Local site (task nb4)

`mage test` covers the generated site without a browser
(`internal/site`, `internal/ctl/open_test.go`: determinism, the
CSP, no remote URL, hostile strings escaped, runs kept). The one
browser check is opt-in, because a test run must not need a
browser:

```sh
CGO_ENABLED=0 IORMESH_SITE_BROWSER=1 go test -run Browser ./internal/site/
# or IORMESH_SITE_BROWSER=/path/to/chromium
```

It writes a site of a model full of hostile strings, loads it in
headless Chrome or Chromium from `file://` at `#/selftest` (the
page renders every route in turn) and fails unless the DOM says
`selftest: ok` and the browser logged no console message — an
exception, or a CSP refusal, which is how a request or an
injected script would show. It passed with Chrome 144 on
2026-11-20. Without Chrome, the same check by hand: build, run
`iormeshctl open`, append `#/selftest` to the URL and see
`selftest: ok N routes` with an empty console (Firefox included).


## LLM introspection eval (task qb4)

milestones.md M5's eval is no part of `mage test` — it needs a model —
but everything around the model is: `internal/eval`'s tests run
the harness against a scripted model and a stub iormeshctl. The
eval itself is two opt-in steps:

```sh
mage evalTrace   # root: one judged TestMicroservices run -> eval/store
IORMESH_EVAL_MODEL_CMD='ollama run MODEL' \
IORMESH_EVAL_MODEL_NAME=MODEL mage eval
```

The model command reads the prompt on stdin and writes its reply
on stdout. The prompt carries the trace's rollup and query
slices, so pick a local model unless the trace is one you may
send elsewhere. The workload's trace holds the scenario's own
files and requests, and also what the JVMs touch on the recording
host: the host name, the java argv (the jars' paths under the
checkout), the JDK's files and the paths of `/etc/hosts` and
`/etc/resolv.conf` with byte counts (never file contents). The
question set and its pinned threshold (0.8) are in
`eval/questions/microservices.yaml`; results land in
`eval/results/<date>-<scenario>-<model>.json`, one file per
`IORMESH_EVAL_MODEL_NAME`, so two models run on one day keep both
(task 9k4). Rerun it on every schema change (milestones.md M5).

A model CLI must send the prompt and nothing more (task wm4).
Claude Code expands an `@<path>` in its prompt — even with
`--tools ''`, even a relative `@Override` against its working
directory — into that file's contents, attached to the request,
and the prompt carries text the harness does not control: the
rollup, query slices and workload source. So the harness escapes
every `@` that starts a word as `\@` in every prompt, and runs
every model command with `CLAUDE_CODE_DISABLE_FILE_MENTIONS=1`
and `CLAUDE_CODE_EVAL_CONFINED=1` in its environment (each was
shown to stop the expansion against a local capture server,
`internal/agenttrial`'s `TestClaudeFileMention`). Name them on a
claude command line all the same, so the command is safe when
run by hand or by another harness. The recommended Claude
command:

```sh
IORMESH_EVAL_MODEL_NAME=claude-sonnet-5-5 \
IORMESH_EVAL_MODEL_CMD="env -C / CLAUDE_CODE_DISABLE_CLAUDE_MDS=1 \
CLAUDE_CODE_DISABLE_AUTO_MEMORY=1 CLAUDE_CODE_DISABLE_GIT_INSTRUCTIONS=1 \
CLAUDE_CODE_DISABLE_FILE_MENTIONS=1 CLAUDE_CODE_EVAL_CONFINED=1 \
claude -p --model claude-sonnet-5-5 --tools '' --strict-mcp-config \
--setting-sources '' --disable-slash-commands --no-session-persistence \
--system-prompt 'Follow the instructions in the user message and \
answer only from it.'" mage eval
```

`env -C /` runs it from `/`, so a mention that slipped through
would resolve against the root directory, not the checkout.

### First runs with real models (task 9k4, 2026-11-26)

Owner-authorised, one run per model, on one store recorded by
`mage evalTrace` on the laptop (Fedora 45, kernel 7.3.4; one
judged `TestMicroservices` run, 4 segment files, 5443 events). The
store was checked before it was sent: four services, three fused
edges, the paths listed above. Each model ran with no tools and no
context files, from `/`. These are the command lines as run; the
Claude one predates task wm4 and lacks the two file-mention
variables and `--disable-slash-commands` — use the recommended
command above, not this one:

```sh
IORMESH_EVAL_MODEL_NAME=claude-sonnet-5-5 \
IORMESH_EVAL_MODEL_CMD="env -C / CLAUDE_CODE_DISABLE_CLAUDE_MDS=1 \
CLAUDE_CODE_DISABLE_AUTO_MEMORY=1 CLAUDE_CODE_DISABLE_GIT_INSTRUCTIONS=1 \
claude -p --model claude-sonnet-5-5 --tools '' --strict-mcp-config \
--setting-sources '' --no-session-persistence --system-prompt '...'" \
mage eval
IORMESH_EVAL_MODEL_NAME=glm-5.3 \
IORMESH_EVAL_MODEL_CMD="env -C / pi -p --no-tools --no-session \
--no-context-files --no-extensions --no-skills --no-prompt-templates \
--no-themes --no-approve --model ollama-cloud/glm-5.3:cloud \
--system-prompt '...'" mage eval
```

(The system prompt was `Follow the instructions in the user
message and answer only from it.`; Claude Code still adds its own
session block — working directory `/`, OS — to every request.)

Could a mention have attached a laptop file to the Claude run
(checked in task wm4, after the store was deleted)? The prompts
were not logged; what remains says no. The question set and both
results files (answers and actions) hold no `@` at all. The
`QUERY` actions were `-file`, `-conn` and `-method` slices: NDJSON
whose paths sit inside JSON strings, after a `"`, where Claude
Code does not expand a mention (shown against the capture server,
as `{@…}` and `x@…` are not). The one `SOURCE` file read,
`scenarios/Microservices.java`, holds four word-starting
`@Override` annotations — and the CLI does treat `@Override` as a
mention, a relative path: from a directory holding a file named
`Override`, claude 2.1.332 attached it (capture server again).
The run's working directory was `/`, and no `/Override` exists, so
nothing was attached; nothing in `workload/src` starts a word
with `@/`, `@~` or `@./`, only `@Override`, `@FunctionalInterface`
and `@param`, none of which name a file in `/`. The rollup and the
slices themselves cannot be re-read. The fix escapes all of these
(`\@Override`), and the guard variables turn the expansion off.

| Question | Claude Sonnet 5.5 | GLM 5.3 (756B, ollama cloud) |
|---|---|---|
| order-file-writer | PASS (1 turn) | PASS (1) |
| orders-config-reader | PASS (2) | PASS (1) |
| gateway-chain | PASS (2) | FAIL (5) |
| orders-next-call | PASS (2) | PASS (4) |
| http-call-method | PASS (2) | PASS (3) |
| billing-cert-reader | PASS (1) | PASS (1) |
| inventory-paths | PASS (1) | PASS (1) |
| billing-record | PASS (2) | PASS (2) |
| tls-hops | PASS (1) | PASS (1) |
| gateway-cleanup | PASS (2) | PASS (1) |
| **Score** (threshold 0.80) | **1.00, passed** | **0.90, passed** |

Results: `eval/results/2026-11-26-microservices-claude-sonnet-5-5.json`
and `eval/results/2026-11-26-microservices-glm-5.3.json`. Both
models pass the pinned threshold, so the eval criterion of M5
holds (milestones.md M5).

GLM's one failure is the matcher, not the views or the query: its
chain was right (`gateway → orders → inventory → billing`), but
its explanation went on to say that orders calls inventory and
that the gateway later "re-reaches inventory", which the
question's `answer_not_regex` (written to catch a chain told out
of order) also matches. The question set was left as pinned.
GLM also once sent three `QUERY` lines glued into one line (a
model formatting slip) and went on with other queries.


## Performance baselines

S3 (decisions.md, decision #25) measures overhead against a load the
workload can state in advance: `syscall-burst`, a tight loop of
1-byte `write` + `read` pairs on per-thread temp files — the
most syscalls per unit of real work, so the tracer's per-event
cost is at its largest. Run it standalone:

```sh
mage workloadBuild
java -jar bin/workload.jar syscall-burst --ops 1000000 --threads 4
```

After the PID it prints one result line,
`syscall-burst ops=N threads=T syscalls=2N elapsed_ns=E`; E
times the loops only, not JVM startup or the file opens. Its
expect.json declares only the marker bracket: the event count
scales with `--ops` and the threads interleave freely, so the
spike asserts counts, not a golden.

`scripts/perf-baseline.sh` (lifted from ior) records and
compares baselines under `perf/`:

```sh
scripts/perf-baseline.sh record [label]   # default label: short HEAD
scripts/perf-baseline.sh compare OLD NEW  # benchstat, labels or paths
```

`record` builds the jar, runs the burst untraced `PERF_COUNT`
times (default 6, the fewest benchstat builds a confidence
interval from) at each thread count in `PERF_THREADS` (default
`1 4 16`) with `PERF_OPS` ops (default 1,000,000), and writes
`perf/burst-<label>.txt`: a provenance header (commit, kernel,
CPU, JDK, load average) and one benchstat line per sample,
`BenchmarkSyscallBurst/mode=untraced/threads=T` with ns/op and
ns/syscall. As in ior, a tree with uncommitted changes gets a
`-dirty` label, an existing baseline is never overwritten
unless `PERF_FORCE=1`, and a tree that changes while recording
writes nothing — so while other work edits the checkout, record
from a clean one (`git worktree add --detach DIR` and run DIR's
script, then copy the file into `perf/`). Record on an idle
host; the header's load average says whether it was. Baselines
are committed so a later change is judged against the numbers
that motivated it. S3's traced and bystander runs add their own
`mode=` lines.


## Independent parquet readers (task ee4)

The events segment file has its own encoder, so `mage test` alone
proves only that `parquet-go` reads it. `TestIndependentReaders`
(`internal/segment/indep_test.go`) also reads it with pyarrow and
DuckDB — every value against the NDJSON of the same events, and
DuckDB's `IS NULL` on every nullable column — when
`IORMESH_PARQUET_CHECK` names a Python venv holding both:

```sh
python3 -m venv /tmp/pq && /tmp/pq/bin/pip install pyarrow duckdb
IORMESH_PARQUET_CHECK=/tmp/pq mage test
```

Without it the test skips: neither reader is a build dependency.
Run it after any change to `internal/segment`'s events encoder.

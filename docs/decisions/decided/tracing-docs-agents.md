# Decided — container ids, pre-session connections, docs and agents

Part of [Decided](../decided.md), in [Decisions](../../decisions.md).

- **#34 The pod sandbox's container id is shown bare** — the
  pod sandbox's `pause` process sits in a container cgroup of
  its own (`cri-containerd-<sandbox id>.scope` on k3s on el9,
  task 6k4), so an in-pod tracer stamps its events with the
  sandbox's runtime id, like any other container's
  (`k8s.ContainerOf`). No container status lists that id —
  `containerStatuses`, `initContainerStatuses` and
  `ephemeralContainerStatuses` name containers only, and `kubectl
  get pod -o json` carries no sandbox id at all — so the pod's
  container map (task jk4) never names it and the build shows
  the bare id, as for any id the map lacks (task fm4). That is
  the decision, not a gap to fill: pause does no traced I/O, so
  the id is seen at most on a process line (pid 1 to a sidecar
  tracer with `shareProcessNamespace`). Rejected: recognising
  the sandbox by its `comm` `pause` — the infra image is the
  runtime's configuration (containerd's `sandbox_image`, CRI-O's
  `pause_image`, any binary name), CRI-O drops the infra
  container altogether unless the pod shares its PID namespace
  (`drop_infra_ctr`), and any app may name a process `pause`, so
  the name would be a guess an app can forge; labelling every
  id the map lacks as the sandbox — a container that restarted
  more than once between two collects is also missing (the
  status keeps only the last `lastState.terminated`), so an
  unknown id is not a sandbox; asking the runtime (CRI
  `PodSandboxStatus`) — the tracer's only host mount is tracefs
  (#13), no CRI socket, and the controller talks to the API
  server alone.
- **#35 Connections open before the session are seeded** (task
  cm4). A TCP socket registers its owner only at connect or
  accept (`sock_owner_map`), so a connection a target opened
  before the session — a keep-alive client, a connection pool
  — gave no state record, its reads and writes no conn id, and
  the build no edge until it reconnected (Rocky 9.9, task 4k4:
  726 events, 0 edges). Now the tracer seeds such sockets:
  right after the probes attach and the start targets are in
  (a pattern's start scan included), and again whenever it
  adopts a process that was already running (a `-listen-port`
  session's restarted owner), userspace runs two BPF task-file
  iterators once each (`iter/task_file`, Linux 5.8; el9 has
  it) by reading them to the end; nothing stays attached. The
  first, `iormesh_seed_listen`, notes the (local address,
  port) of every listening TCP socket of any task the tracer's
  PID namespace shows. The second, `iormesh_seed_sock`, takes
  every TCP socket of a traced target that is in no state of
  CLOSE or LISTEN and not registered yet, registers it as a
  connect would (pid, fd, socket inode; `BPF_NOEXIST`, so a
  socket a connect or accept registered first is not seeded,
  and a second seeding adds nothing; an accept whose exit runs
  after the walk seeded its socket still emits its accept
  snapshot — a second snapshot of one socket, harmless: the
  correlator binds the same fd to the same socket), and emits
  a `tcp_state` record
  whose old state is a marker beyond every TCP state:
  `TCP_STATE_INITIAL_SERVER` (258) when a listener holds the
  socket's local port, on its address or the wildcard (or the
  socket is in SYN_RECV), else `TCP_STATE_INITIAL_CLIENT`
  (257). It carries the 4-tuple and the four `tcp_sock` fields
  (#26), read like the accept snapshot's: re-read until two
  reads agree, else torn. The correlator binds the fd to the
  socket (as a connect's first record does), takes the socket
  as established, and writes a `tcp_state` event with
  `args.sock_snapshot` `initial_client` or `initial_server`.
  The merge reads that value as role evidence, keys the half
  from it as from an accept snapshot (only when no locked
  state change keys it), and takes the half's `Established`
  as an upper bound: such a half is a candidate for a partner
  established no later than its own time plus the window, two
  of them always — the ISN pair alone already tells reused
  tuples apart (S2); the window only guards the ISN clock's
  wrap. `report` and the service database take the role too.
  Why the listener test: the kernel keeps no "accepted" mark
  on an established socket, and the key's SYN correction
  (#26) needs the role; a local port some listener holds is
  how `ss`-based tools tell a server end. Why iterators: the
  owner map is keyed by `struct sock` address, which /proc
  and sock_diag never show; the iterator runs in the kernel
  over the tracer's own PID namespace, so a pod's tracer seeds
  its pod's processes only. What stays: a client whose local
  port a listener of another network namespace also holds, or
  one bound to a listening port on purpose, reads as a server
  — its key then misses by one and the half stays `no_match`,
  never a wrong fuse (likewise a server whose listener closed,
  read as a client); calls between the attach and the seeding
  walk (milliseconds) name no connection; bytes moved before
  the session are in no sum, so the stream check fails for
  such a direction as it must (the counter proof still
  holds); and an initial half gives no handshake clock bound.
  Rejected: inferring the role at the merge from the two
  ends' raw ISN pairs — it needs both ends traced, and a
  single host's report needs the role too; registering lazily
  on a socket's first read or write — an fd walk on every data
  call; duplicating the target's descriptor (`pidfd_getfd`)
  to snapshot it through a test run — the tracer would hold
  the target's socket, delaying its close; keeping it a
  documented limitation — pooled clients are the common case.
- **#36 Large docs are an index plus a folder of parts** — a
  topic doc in `docs/` that grows past the limit below keeps
  its path and becomes an index: its intro, then a "Parts"
  list with one summary line per part and the sections each
  part holds. The sections move to `docs/<topic>/<part>.md`
  (for a spike, `docs/spikes/<spike>/`), each part opening
  with a "Part of" line back to its index. A part that grows
  past the same size, and has sections of its own, becomes an
  index of its own the same way (`docs/<topic>/<part>.md` plus
  `docs/<topic>/<part>/`). Agents and readers load the index
  first and open only the part they need, instead of a
  1,500–6,500-line file; every citation of the form
  `docs/<topic>.md "Section"` or `docs/decisions.md #N` keeps
  resolving through the index, and links to a moved section
  point at its part. `docs/decisions.md` also lists every
  decision by number with the part that holds it, so the next
  free number stays one glance away. A section heading kept as
  a part's title keeps its anchor. Docs under the limit stay
  one file. The limit is exact and checked (task sm4): every
  Markdown file of the repository — `docs/**/*.md`,
  `AGENTS.md`, `CHANGELOG.md`, `spikes/*/NOTES.md` — has at
  most 600 lines, and `README.md` at most 150, the "README
  stays lean" rule made a number. `internal/buildgate`'s
  `TestDocsUnderSizeLimit` fails naming each file over its
  limit and its line count. Directories that are not the
  repository's docs are skipped: dot directories (`.git`, and
  `.claude`, which holds other worktrees), `bin/` (build
  outputs, #30), `vendor/`, `node_modules/`. No file is exempt;
  an exemption would be a reviewed entry with its reason in the
  gate's table, and an entry whose file is no longer over the
  limit fails. A file over the limit splits; the limit is not
  raised to fit it. Rejected: one file per `##` section
  everywhere — dozens of 15-line files for testing.md's task
  records, which are read together; splitting by line count —
  parts would start mid-section; a check by hand — the rule
  broke in the next commit after it landed (decided.md reached
  710 lines).
- **#37 A smaller agent installs and uses iormesh from the docs
  and a skill** — owner decision (2026-11-26, task lm4; built
  and accepted as M8, [milestones/m8.md](../../milestones/m8.md)).
  iormesh must be easy to install and use for a less capable
  agent than the ones that built it: Claude Sonnet 5.5 at
  medium effort (`claude -p`) and glm-5.3-flash (the `pi`
  coding agent, ollama cloud). What ships, what is tested and
  the numbers:
  - **The skill.** The repository ships an agent skill for
    using iormesh, `docs/skills/iormesh/SKILL.md` (the owner's
    choice of home), written by the skill-maintenance skill's
    rules (task om4). One file loads in both agents: the
    operator copies or links the folder into Claude Code's
    skills directory (`~/.claude/skills/iormesh/`) or pi's
    (`~/.pi/skills/iormesh/`), or passes it per run (`pi
    --skill docs/skills/iormesh`). Its rules: (1) before
    every step it tells the user what is about to happen —
    which host, which commands, what they change, what data
    leaves the machine — and waits for approval; a step may
    hold several commands when the explanation names each;
    (2) at the start the user picks a mode: *automatic* (the
    agent runs each approved command itself) or *guided* (the
    agent only names the commands; the user types them and
    pastes the output back). The guide stays the one home of
    the path; the skill carries the protocol and points into
    the guide, it does not re-teach it.
  - **Where.** The throwaway iom VMs on f3 (Rocky 9.9, the
    main target): the agent's shell lands on `iom1`, which
    plays the controller (the guide's laptop); `iom2` is the
    traced host. Each agent runs on the laptop, but its only
    shell is SSH into the VMs (sudo there); it gets no tool
    that reads, writes or runs anything on the laptop.
  - **Path tested: the release archive** (guide chapter 3,
    "Get a release": download, `sha256sum -c`, sign, install).
    Building from source needs Go, mage, Podman and a libbpfgo
    checkout — the builder's path, not the user's — and a
    smaller agent failing there would grade the build, not
    iormesh's use. Until a release is published, the archive
    is `mage release` of the commit under test, served to the
    VMs at a URL the task prompt names, as a release page
    would.
  - **What the agent gets:** the fixed task prompt and
    question set (`eval/agent-trial/`), a copy of README.md,
    `docs/guide/` and the skill at the commit under test, and
    VMs prepared as chapter 2's trust steps leave them —
    `iom1`'s SSH key on `iom2`, `iom2`'s host key in `iom1`'s
    `known_hosts`, sudo without a password, and a throwaway
    signing-key password in `$IORMESH_MINISIGN_PASSWORD` —
    because each of those is a **Human step** in the guide an
    agent must not take. Everything else (packages, the key —
    `iormeshctl keygen` from that password, #38 — the release,
    bootstrap, deploy) the agent does itself. The
    harness starts the test workload's microservices scenario
    on `iom2` before the run: it is the application under
    observation, as an operator's service would be. Links out
    of the guide into the topic docs are not copied: a run
    that needs one has found a gap in the guide.
  - **Help** is any human message during a run; any message
    from the scripted user beyond the mode choice, an approval
    and (guided) the verbatim output; any file placed on the
    VMs beyond the list above; any rerun of the same commit
    to try again. A run with help fails.
  - **Approval in the unattended trial:** a scripted user
    approves every explanation that names the host, the
    commands, the change and the outgoing data, and nothing
    else. A command run that the latest approved explanation
    did not name fails the run, however good its answers. In
    guided mode the agent has no shell; the harness types
    exactly the commands the agent named, on the named host,
    and pastes back the output.
  - **Budget and score:** 150 agent turns (one model reply,
    with or without a tool call; the scripted user's replies
    are not turns) and 60 minutes of wall time per run; past
    either the run fails. The question set holds 10 questions
    with answers known from the scenario's declarations; the
    pass is 8 of 10 (0.8), and only after the install and the
    trace succeeded (a built mesh holding the workload's
    cross-service edges). Why these numbers: the guide's path
    from chapter 2 to chapter 8 is a few dozen commands, and
    M5's eval allows 8 turns a question (80 for ten); 150
    leaves room for a smaller model's retries on top of both,
    not for wandering. Segments rotate each minute (#16), so the
    trace needs a few minutes before collect, and ollama
    cloud's replies are slow; 60 minutes covers both with
    room. 0.8 is M5's pinned threshold: the same bar for the
    same kind of question, now asked after a self-made
    install.
  - **Acceptance runs four cells** — both agents, both modes —
    once each, on one commit; all four must pass. A failed
    cell becomes a task with its transcript and is rerun only
    after a change to the docs, the skill or iormesh. The
    questions are never reworded, dropped or re-scored to make
    a run pass; they change only when the scenario changes,
    recorded with the reason.
  - **Data.** Both providers see everything the agent sees:
    the prompt, the docs, and all command output from the VMs
    — host names and addresses of the f3 VMs, the synthetic
    workload's trace, the throwaway key's password when it is
    printed. Claude Sonnet sends it to Anthropic, glm-5.3-flash
    to ollama cloud. Nothing from the laptop reaches either;
    the VMs hold no real data and are destroyed after the
    trial. The owner authorises this for the trial only; it
    does not relax the rule that real trace data stays local.

  Why: the guide is written for "a human, or an agent working
  with one" (docs/guide/README.md), and only a run proves an
  agent can follow it; a capable agent fills gaps a smaller one
  falls into, so the smaller one is the test. The approval
  protocol makes an agent safe to point at real hosts, and
  guided mode serves an operator who will not hand an agent a
  shell. The harness is task mm4, the trial task nm4. Rejected:
  building from source as the tested path (above); scoring by
  install only — iormesh is used for its answers; an agent shell
  on the laptop — it holds the operator's keys and data, the
  very thing the trial must not expose; tuning the questions
  after a failure — it hides the gap the trial exists to find.

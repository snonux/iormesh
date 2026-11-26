// Package agenttrial is M8's agent trial harness (docs/milestones/m8.md,
// decision #37): one agent — Claude Sonnet 5.5 at medium effort through
// `claude -p`, or glm-5.3-flash through `pi -p` — in one mode of the
// iormesh skill (automatic or guided) installs iormesh from the release
// archive on throwaway VMs, traces the microservices scenario and
// answers a fixed question set, with a scripted user and within a turn
// and time budget. cmd/iormeshtrial and `mage agentTrial` run it.
//
// The sandbox. The agent's only reach is the VMs (#37, "Where"). The
// agent CLI therefore gets no tool at all (claude's --tools with an
// empty list, pi's --no-tools), no context files, memory, settings, MCP servers,
// extensions or skills of the laptop, our system prompt in place of its
// own, and an empty working directory outside the checkout; claude also
// has its "@path" file mentions disabled (ClaudeEnv), and every message
// the harness sends has a word-starting "@" escaped (eval.DefuseMentions),
// since the messages carry VM output the agent steers. It acts by
// text, one action per reply, which the harness carries out:
//
//	(an explanation in the skill's form)   the scripted user answers it
//	RUN <command>                          an approved command, on its step's VM
//	READ <path>                            a file of the docs copy
//	ANSWER <id>: <text>                    the answers, all in the final reply
//
// Why not the CLIs' own shell tool behind an allowlist: pi has none (its
// bash tool is a full local shell and its read tool reads any path), and
// claude's Bash(prefix) rule is a permission check, not a sandbox; only
// a reply-per-turn protocol also lets the harness count turns as #37
// does (one model reply) and check a command against the approved
// explanation before it runs rather than after. RUN goes through Shell,
// which runs ssh with the host checked against the VMs and the command
// as ssh's one remote argument, never through a local shell; READ goes
// through eval.SourceTree (os.Root) over a copy of README.md, the guide
// and the skill, nothing else of the checkout.
//
// The scripted user (#37, "Help", "Approval in the unattended trial")
// says only: the mode, given in answer to the agent's first message
// that is not a READ; "yes" to an explanation that names the host, the
// commands, the change and the outgoing data, "change" to one that does
// not; and in guided mode, after the "yes", the output of exactly the
// named commands, typed on the named VM. A RUN the latest approved
// explanation did not name, a RUN before any approval and any RUN in
// guided mode fail the run before anything runs. Every command, the
// agent's, the user's and the harness's, is logged (commands.jsonl), and
// every message (transcript.jsonl), under eval/agent-trial/runs/.
//
// Scoring is M5's: the questions (eval/agent-trial/questions.yaml) load
// with eval.LoadSet and each answer is scored by eval.Question.Score.
// The run passes when it broke no rule, stayed within 150 turns and 60
// minutes, the controller's `iormeshctl show edges` holds a fused edge
// to each of the workload's called services, and the score reaches the
// set's pinned threshold (0.8). The result goes to
// eval/agent-trial/results/<date>-<model>-<mode>.json.
package agenttrial

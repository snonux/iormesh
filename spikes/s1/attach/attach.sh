#!/usr/bin/env bash
# Throwaway (S1, task xa4): decision #2 experiments on one JDK. Starts the
# workload gated (it waits at its startup file, i.e. a long-running JVM
# whose classes are already loaded), live-attaches the S1 agent, opens
# the gate, and shows the target's stderr (the JEP 451 warning) and
# whether the agent's hooks ran (the .recs it dumps at shutdown).
#
# Usage: attach.sh <JAVA_HOME> <agent dir> <outdir> <case>
#   default   — no flags: JEP 451 warning expected
#   allow     — -XX:+EnableDynamicAgentLoading: silent
#   deny      — -XX:-EnableDynamicAgentLoading: attach must fail
#   root      — target runs as the invoking user, attacher as root
#   premain   — -javaagent at launch, for comparison
set -euo pipefail

readonly jh="${1:?usage: attach.sh JAVA_HOME AGENTDIR OUTDIR CASE}"
readonly ad="${2:?agent dir}"
readonly out="${3:?outdir}"
readonly kase="${4:?case}"
here="$(cd "$(dirname "$0")" && pwd)"
readonly here
readonly jar="$here/../../../workload.jar"
readonly aargs="$ad/bridge.jar,$out/$kase"

mkdir -p "$out"
rm -f "$out/$kase".{gate,stdout,stderr,recs,hooks}
flags=()
case "$kase" in
  allow) flags=(-XX:+EnableDynamicAgentLoading) ;;
  deny) flags=(-XX:-EnableDynamicAgentLoading) ;;
  premain) flags=("-javaagent:$ad/agent.jar=$aargs") ;;
esac
IORMESH_WORKLOAD_STARTUP_FILE="$out/$kase.gate" "$jh/bin/java" "${flags[@]}" \
  -jar "$jar" file-basic --dir "$out" > "$out/$kase.stdout" 2> "$out/$kase.stderr" &
readonly jpid="$!"
trap 'kill "$jpid" 2>/dev/null || true' EXIT
for _ in $(seq 100); do [[ -s "$out/$kase.stdout" ]] && break; sleep 0.05; done
sleep 0.5   # let the JVM settle at the gate: a "long-running" target

attacher=("$jh/bin/java" "$here/Attach.java" "$jpid" "$ad/agent.jar" "$aargs")
case "$kase" in
  premain) echo "attach: skipped (-javaagent)" ;;
  root) sudo -n "${attacher[@]}" || echo "attach as root failed: $?" ;;
  *) "${attacher[@]}" || echo "attach failed: $?" ;;
esac
touch "$out/$kase.gate"
wait "$jpid" || true
echo "--- target stderr"
cat "$out/$kase.stderr"
echo "--- agent brackets recorded: $(wc -l < "$out/$kase.recs" 2>/dev/null || echo none)"

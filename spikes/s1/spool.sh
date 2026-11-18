#!/usr/bin/env bash
# Throwaway (S1, task xa4): the #27 transport under load — syscall-burst
# with the agent in ring mode while spoolread.py drains the ring
# concurrently, so the ring laps; prints the reader's counts and the
# header's drop counters. Usage:
#   spool.sh <JAVA_HOME> <agent dir (build.sh)> <log2 ring bytes> <ops> <outdir>
set -euo pipefail

readonly jh="${1:?usage: spool.sh JAVA_HOME AGENTDIR LOG2 OPS OUTDIR}"
readonly ad="${2:?agent dir}"
readonly k="${3:?log2 ring size}"
readonly ops="${4:?ops}"
readonly out="${5:?outdir}"
here="$(cd "$(dirname "$0")" && pwd)"
readonly here

mkdir -p "$out"
rm -f "$out/burst.spool"
"$jh/bin/java" -javaagent:"$ad/agent.jar=$ad/bridge.jar,$out/burst,ring:$k" \
  -jar "$here/../../workload.jar" syscall-burst --ops "$ops" --dir "$out" \
  > "$out/burst.stdout" 2> "$out/burst.stderr" &
readonly jpid="$!"
trap 'kill "$jpid" 2>/dev/null || true' EXIT
# premain creates the spool before main() runs, so it exists by the time
# the workload prints its PID line.
for _ in $(seq 100); do
  [[ -s "$out/burst.stdout" ]] && break
  sleep 0.05
done
python3 "$here/spoolread.py" ring "$out/burst.spool" --follow "$jpid"
grep elapsed_ns "$out/burst.stdout"

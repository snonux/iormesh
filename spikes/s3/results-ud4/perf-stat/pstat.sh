#!/usr/bin/env bash
# pstat.sh BIN SPOOL(0/1) OPS: one traced burst (THREADS, default 1),
# perf stat of the tracer from its ready file to its exit: task-clock,
# cycles and instructions per event (task ud4; throwaway spike code).
set -euo pipefail
repo=$(cd "$(dirname "$0")/../../../.." && pwd)
bin=$1 spool=$2 ops=$3
W=$(mktemp -d)
jto=
fl=()
if [ "$spool" = 1 ]; then
    sudo -n mkdir -m 1733 "$W/spool"
    jto="-javaagent:$repo/iormesh-agent.jar=spool=$W/spool"
    fl=(-spool "$W/spool")
fi
JAVA_TOOL_OPTIONS=$jto IORMESH_WORKLOAD_STARTUP_FILE=$W/gate taskset -c 0-2,4-6 \
    java -jar $repo/workload.jar syscall-burst --ops "$ops" --threads "${THREADS:-1}" \
    --dir "$W" >"$W/out" 2>"$W/err" &
until [ -s "$W/out" ]; do sleep 0.1; done
P=$(head -1 "$W/out")
sudo -n taskset -c 3 "$bin" run -pid "$P" -project s3 -out "$W/trace" -duration 2m \
    -ready "$W/ready" -aggregate=false "${fl[@]}" 2>"$W/tracer.err" &
until [ -e "$W/ready" ]; do sleep 0.1; done
TP=$(pgrep -f "^$bin run -pid $P")
sudo -n perf stat -x, -e task-clock,cycles:u,cycles:k,instructions:u,instructions:k,page-faults,context-switches \
    -p "$TP" -o "$W/perf" &
sleep 0.5
touch "$W/gate"
wait
ev=$(wc -l <"$W/trace/events.ndjson"); bel=$(grep -o "elapsed_ns=[0-9]*" "$W/out" | cut -d= -f2)
awk -F, -v ev="$ev" -v b="$(basename "$bin")" -v t="${THREADS:-1}" -v bel="$bel" -v l="$(cut -d' ' -f1 /proc/loadavg)" \
    '$3!=""{v[$3]=$1} END{printf "%s t=%s burst_ms=%d load=%s per_event: task_ns=%.0f cyc_u=%.0f cyc_k=%.0f ins_u=%.0f ins_k=%.0f pf=%.4f cs=%.4f\n", b, t, bel/1e6, l, v["task-clock"]*1e6/ev, v["cycles:u"]/ev, v["cycles:k"]/ev, v["instructions:u"]/ev, v["instructions:k"]/ev, v["page-faults"]/ev, v["context-switches"]/ev}' "$W/perf"
sudo -n rm -rf "$W"

#!/usr/bin/env bash
# A/B of task xe4's tcp_seq capture on a TCP loopback load (tcpload.c),
# unfolded: per run, the load untraced, under the baseline tracer and
# under the new one, pinned like spikes/s3/measure.sh (tracer CPU 3, load
# 0-2,4-6). One sample line per run and tracer.
# Usage: tcpmeasure.sh OUT RUNS BASE_TRACER NEW_TRACER [OPS] [SIZE]
set -euo pipefail
out=$1 runs=$2 base=$3 new=$4 ops=${5:-200000} size=${6:-64} pace=${7:-0}
dir=$(cd "$(dirname "$0")" && pwd)
hz=$(getconf CLK_TCK)
cpu_ms() { awk -v hz="$hz" '{printf "%d", ($14 + $15) * 1000 / hz}' "/proc/$1/stat"; }
wait_for() { local i; for ((i = 0; i < 1200; i++)); do [ -e "$1" ] && return 0; sleep 0.1; done; return 1; }
first_line() { local i; for ((i = 0; i < 1200; i++)); do [ -s "$1" ] && { head -1 "$1"; return 0; }; sleep 0.05; done; return 1; }
result_line() { local i; for ((i = 0; i < 2400; i++)); do grep -m1 '^tcpload ' "$1" && return 0; sleep 0.05; done; return 1; }

one() {
    local tracer=$1 tag=$2 work lpid ljob tjob pjob ipid ready_cpu total res events seqs
    work=$(mktemp -d)
    mkfifo "$work/gate"
    exec 7<>"$work/gate"
    taskset -c 0-2,4-6 "$dir/tcpload" "$ops" "$size" "$pace" <"$work/gate" >"$work/out" &
    ljob=$!
    lpid=$(first_line "$work/out")
    if [ "$tracer" != none ]; then
        sudo -n taskset -c 3 /usr/bin/time -f '%U %S' -o "$work/time" "$tracer" run -pid "$lpid" \
            -project s3 -out "$work/trace" -duration 10m -ready "$work/ready" -aggregate=false \
            2>"$work/tracer.err" &
        tjob=$!
        wait_for "$work/ready"
        ipid=$(pgrep -f -- "^$tracer run -pid $lpid ")
        ready_cpu=$(cpu_ms "$ipid")
        sudo -n perf stat -x, -e instructions:u,instructions:k -p "$ipid" -o "$work/tperf" 2>/dev/null &
    fi
    sudo -n perf stat -x, -e instructions:u,instructions:k -p "$lpid" -o "$work/perf" 2>/dev/null &
    pjob=$!
    sleep 0.3
    echo go >&7
    res=$(result_line "$work/out")
    wait "$ljob"
    exec 7>&-
    wait "$pjob" || true
    local ik
    ik=$(awk -F, '/instructions:k/{print $1}' "$work/perf")
    if [ "$tracer" = none ]; then
        echo "tag=$tag $res insn_k=$ik load=$(cut -d' ' -f1 /proc/loadavg)" >>"$out"
    else
        wait "$tjob"
        total=$(awk '{printf "%d", ($1 + $2) * 1000}' "$work/time")
        sleep 0.5
        local tu tk
        tu=$(awk -F, '/instructions:u/{print $1}' "$work/tperf")
        tk=$(awk -F, '/instructions:k/{print $1}' "$work/tperf")
        events=$(wc -l <"$work/trace/events.ndjson")
        seqs=$(grep -c '"tcp_seq"' "$work/trace/events.ndjson" || true)
        echo "tag=$tag $res insn_k=$ik tracer_run_cpu_ms=$((total - ready_cpu)) events=$events tracer_insn_u=$tu tracer_insn_k=$tk" \
            "tcp_seq_events=$seqs lost=$(sed -n 's/.*lost records \([0-9]*\).*/\1/p' "$work/tracer.err")" \
            "load=$(cut -d' ' -f1 /proc/loadavg)" >>"$out"
    fi
    sudo -n rm -rf -- "$work"
}

for ((r = 1; r <= runs; r++)); do
    one none untraced
    one "$base" base
    one "$new" new
done

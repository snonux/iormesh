#!/usr/bin/env bash
# ab.sh OUT ROUNDS AGENT THREADS: interleaved A/B of two tracer builds
# (BASE and NEW, paths to binaries) on M4's segment-writer path, one
# measure.sh run per round and build, base first (task ee4, after ud4's
# ab.sh; throwaway spike code). Prints one line per traced run: CPU time,
# instructions and cycles (user + kernel) per emitted event, and load.
set -euo pipefail
repo=$(cd "$(dirname "$0")/../../.." && pwd)
out=$1 rounds=$2 agent=$3 threads=$4 base=${BASE:?} new=${NEW:?}
mkdir -p "$out"
for r in $(seq 1 "$rounds"); do
    for v in base new; do
        bin=$base
        [ $v = new ] && bin=$new
        S3_SEGMENT=1 S3_PERF=1 S3_MODES=traced S3_RUNS=1 S3_THREADS="$threads" \
            S3_OPS=${OPS:-500000} S3_AGENT=$agent S3_TRACER=$bin \
            S3_TRACER_FLAGS="-aggregate=false" \
            "$repo/spikes/s3/measure.sh" "$out/$v-$r" >"$out/$v-$r.log" 2>&1 ||
            echo "round $r $v failed" >&2
    done
done
for v in base new; do
    cat "$out"/$v-*/samples.txt | grep '^mode=traced' |
        awk -v v=$v '{for(i=1;i<=NF;i++){split($i,kv,"=");f[kv[1]]=kv[2]}
            e=f["events"]
            printf "%s threads=%s ns_per_event=%.0f insn_per_event=%.0f cycles_per_event=%.0f load=%s events=%s lost=%s attributed=%s\n",
                v, f["threads"], f["tracer_run_cpu_ms"]*1e6/e, (f["insn_u"]+f["insn_k"])/e,
                (f["cyc_u"]+f["cyc_k"])/e, f["load"], e, f["lost"], f["attributed"]}'
done

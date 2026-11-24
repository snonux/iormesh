#!/usr/bin/env bash
# ab-valid.sh OUT ROUNDS AGENT THREADS: ab.sh with the validity rules of
# the owner decision of 2026-11-20 (docs/decisions.md #25): a run
# counts only at load <= 10 (the 1-minute load average before it starts and as
# measure.sh records it after), with zero lost records and — with the
# agent — every burst event attributed (attributed >= 2 x ops). Any
# other run is neither pass nor fail: it is kept apart (rejected-*) and
# retried once the load is back under the limit. FLAGS overrides the
# tracer flags (default -aggregate=false; "" runs the default fold).
# LOSSOK=1 drops the zero-loss rule: the plain unfolded burst at 4
# threads overruns the ring by design (decision #25's loss item is
# judged with the fold on), and its budget is CPU only. ATTRIB=0 drops
# the every-event-attributed rule: under the default fold a hot loop's
# calls are folded and never joined, by design.
# Throwaway spike code (task ee4).
set -euo pipefail
repo=$(cd "$(dirname "$0")/../../.." && pwd)
out=$1 rounds=$2 agent=$3 threads=$4 base=${BASE:?} new=${NEW:?}
ops=${OPS:-500000} flags=${FLAGS--aggregate=false} maxload=${MAXLOAD:-10}
lossok=${LOSSOK:-0} attrib=${ATTRIB:-1}
mkdir -p "$out"

load1() { cut -d' ' -f1 /proc/loadavg; }
below() { awk -v l="$1" -v m="$maxload" 'BEGIN{exit !(l <= m)}'; }

# valid reads a run's traced sample and says whether it counts.
valid() {
    local line
    line=$(grep '^mode=traced' "$1/samples.txt" 2>/dev/null) || return 1
    awk -v agent="$agent" -v ops="$ops" -v m="$maxload" -v lossok="$lossok" -v attrib="$attrib" '{
        for (i = 1; i <= NF; i++) { split($i, kv, "="); f[kv[1]] = kv[2] }
        ok = (lossok == 1 || f["lost"] == 0) && f["load"] <= m
        if (agent == 1 && attrib == 1) ok = ok && f["attributed"] >= 2 * ops
        exit !ok }' <<<"$line"
}

# take runs one build until it gets a valid run.
take() {
    local v=$1 bin=$2 r=$3 try=0 dir
    while :; do
        until below "$(load1)"; do sleep 30; done
        dir=$out/$v-$r
        rm -rf "$dir"
        S3_SEGMENT=1 S3_PERF=1 S3_MODES=traced S3_RUNS=1 S3_THREADS="$threads" \
            S3_OPS=$ops S3_AGENT=$agent S3_TRACER=$bin S3_TRACER_FLAGS="$flags" \
            "$repo/spikes/s3/measure.sh" "$dir" >"$dir.log" 2>&1 || true
        valid "$dir" && return 0
        try=$((try + 1))
        mv "$dir" "$out/rejected-$v-$r-$try"
    done
}

for r in $(seq 1 "$rounds"); do
    take base "$base" "$r"
    take new "$new" "$r"
done
for v in base new; do
    cat "$out"/$v-*/samples.txt | grep '^mode=traced' |
        awk -v v=$v '{for(i=1;i<=NF;i++){split($i,kv,"=");f[kv[1]]=kv[2]}
            e=f["events"]
            printf "%s threads=%s ns_per_event=%.0f insn_per_event=%.0f cycles_per_event=%.0f load=%s events=%s lost=%s attributed=%s\n",
                v, f["threads"], f["tracer_run_cpu_ms"]*1e6/e, (f["insn_u"]+f["insn_k"])/e,
                (f["cyc_u"]+f["cyc_k"])/e, f["load"], e, f["lost"], f["attributed"]}'
done
echo "rejected runs: $(find "$out" -maxdepth 1 -name 'rejected-*' | wc -l)"

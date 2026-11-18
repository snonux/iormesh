#!/usr/bin/env bash
# S3 measurement (docs/implementation.md "S3", PLAN.md decision #25):
# target slowdown, bystander cost, tracer CPU, loss and output volume of
# the M1 tracer on the syscall-burst load scenario.
#
# Usage: spikes/s3/measure.sh OUTDIR   (needs sudo -n; run on a quiet host)
# Env:   S3_OPS (default 200000), S3_THREADS ("1 4 16"), S3_RUNS (3)
#
# Every sample prints one line to OUTDIR/samples.txt:
#   mode=<untraced|traced|bystander> threads=T ops=N elapsed_ns=E
#   [tracer_cpu_ms=C lost=L events=K ndjson_bytes=B]
# Throwaway spike code: the numbers, not the script, are the result.
set -euo pipefail

repo=$(cd "$(dirname "$0")/../.." && pwd)
out=${1:?usage: measure.sh OUTDIR}
ops=${S3_OPS:-200000}
threads=${S3_THREADS:-1 4 16}
runs=${S3_RUNS:-3}
jar=$repo/workload.jar
tracer=$repo/iormesh
mkdir -p "$out"
samples=$out/samples.txt
pids=()

cleanup() {
    local p
    for p in "${pids[@]}"; do
        sudo -n kill -INT "$p" 2>/dev/null || kill "$p" 2>/dev/null || true
    done
}
trap cleanup EXIT

die() { echo "measure: $*" >&2; exit 1; }

# wait_file waits up to 120 s for a file to exist (loading the BPF object
# takes seconds, more on a busy host). With a pid, a dead process ends
# the wait at once and its stderr file is shown.
wait_file() {
    local file=$1 pid=${2:-} errf=${3:-}
    local -i i
    for ((i = 0; i < 1200; i++)); do
        [ -e "$file" ] && return 0
        if [ -n "$pid" ] && ! kill -0 "$pid" 2>/dev/null; then
            die "process $pid exited before $file appeared: $(tail -3 "$errf" 2>/dev/null)"
        fi
        sleep 0.1
    done
    die "timed out waiting for $file: $(tail -3 "$errf" 2>/dev/null)"
}

# elapsed_of prints elapsed_ns from a syscall-burst stdout file.
elapsed_of() {
    sed -n 's/^syscall-burst .* elapsed_ns=\([0-9][0-9]*\)$/\1/p' "$1"
}

untraced() {
    local t=$1 work
    work=$(mktemp -d)
    java -jar "$jar" syscall-burst --ops "$ops" --threads "$t" --dir "$work" >"$work/out" 2>/dev/null ||
        die "untraced burst failed"
    echo "mode=untraced threads=$t ops=$ops elapsed_ns=$(elapsed_of "$work/out")" >>"$samples"
    rm -rf -- "$work"
}

# traced runs the burst behind its startup gate, traced by its own PID.
# Tracer CPU is utime+stime from /usr/bin/time (the tracer is sudo's
# child, so time wraps the tracer itself).
traced() {
    local t=$1 work pid tpid
    work=$(mktemp -d)
    IORMESH_WORKLOAD_STARTUP_FILE=$work/gate java -jar "$jar" syscall-burst --ops "$ops" \
        --threads "$t" --dir "$work" >"$work/out" 2>/dev/null &
    pids+=($!)
    wait_file "$work/out"
    pid=$(head -1 "$work/out")
    sudo -n /usr/bin/time -f '%U %S' -o "$work/time" "$tracer" run -pid "$pid" -project s3 \
        -out "$work/trace" -duration 10m -ready "$work/ready" 2>"$work/tracer.err" &
    tpid=$!
    pids+=("$tpid")
    wait_file "$work/ready" "$tpid" "$work/tracer.err"
    touch "$work/gate"
    wait "$tpid" || die "tracer failed: $(tail -3 "$work/tracer.err")"
    traced_line "$t" "$work"
    sudo -n rm -rf -- "$work"
}

traced_line() {
    local t=$1 work=$2 cpu lost events bytes
    cpu=$(awk '{printf "%d", ($1 + $2) * 1000}' "$work/time")
    lost=$(sed -n 's/.*lost records \([0-9][0-9]*\).*/\1/p' "$work/tracer.err")
    events=$(wc -l <"$work/trace/events.ndjson")
    bytes=$(stat -c %s "$work/trace/events.ndjson")
    echo "mode=traced threads=$t ops=$ops elapsed_ns=$(elapsed_of "$work/out")" \
        "tracer_cpu_ms=$cpu lost=$lost events=$events ndjson_bytes=$bytes" >>"$samples"
}

# bystander runs the burst untraced while a tracer is attached to another,
# idle process: the burst's syscalls still hit the tracepoints, which
# filter it out. The difference to untraced is the host-wide tax.
bystander() {
    local t=$1 work idle tpid
    work=$(mktemp -d)
    sleep 600 &
    idle=$!
    pids+=("$idle")
    # The idle-target tracer's CPU is the fixed cost (object load, attach,
    # liveness polls) that traced runs subtract to get per-event cost.
    sudo -n /usr/bin/time -f '%U %S' -o "$work/time" "$tracer" run -pid "$idle" -project s3 \
        -out "$work/trace" -duration 10m -ready "$work/ready" 2>"$work/tracer.err" &
    tpid=$!
    pids+=("$tpid")
    wait_file "$work/ready" "$tpid" "$work/tracer.err"
    java -jar "$jar" syscall-burst --ops "$ops" --threads "$t" --dir "$work" >"$work/out" 2>/dev/null ||
        die "bystander burst failed"
    kill "$idle"
    wait "$tpid" || true
    echo "mode=bystander threads=$t ops=$ops elapsed_ns=$(elapsed_of "$work/out")" \
        "tracer_cpu_ms=$(awk '{printf "%d", ($1 + $2) * 1000}' "$work/time")" >>"$samples"
    sudo -n rm -rf -- "$work"
}

header() {
    {
        echo "# S3 samples $(date -u +%Y-%m-%dT%H:%M:%SZ) commit $(git -C "$repo" rev-parse --short HEAD)"
        echo "# kernel $(uname -r); cpu $(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2- | xargs) x$(nproc)"
        echo "# java $(java -version 2>&1 | head -1); workdir fs $(df -T "${TMPDIR:-/tmp}" | awk 'NR==2{print $2}')"
        echo "# ops=$ops threads=$threads runs=$runs"
    } >>"$samples"
}

main() {
    [ -x "$tracer" ] && [ -f "$jar" ] || die "run 'mage build workloadBuild' first"
    header
    local -i r
    local t
    for ((r = 1; r <= runs; r++)); do
        for t in $threads; do
            echo "# run $r threads $t loadavg $(cut -d' ' -f1-3 /proc/loadavg)" >>"$samples"
            untraced "$t"
            traced "$t"
            bystander "$t"
        done
    done
}

main

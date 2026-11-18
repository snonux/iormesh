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
# pids and dirs hold only what is still live: forget() drops a pid once
# it was waited for, so the trap never signals a recycled PID.
pids=()
dirs=()

# cleanup sends TERM, not INT: background children of a non-interactive
# shell start with SIGINT ignored.
cleanup() {
    local p d
    for p in "${pids[@]}"; do
        sudo -n kill -TERM "$p" 2>/dev/null || true
    done
    for d in "${dirs[@]}"; do
        sudo -n rm -rf -- "$d"
    done
}
trap cleanup EXIT

forget() {
    local p keep=()
    for p in "${pids[@]}"; do
        [ "$p" = "$1" ] || keep+=("$p")
    done
    pids=("${keep[@]}")
}

# newdir creates a work dir and registers it for cleanup. It sets the
# global $work instead of printing it: called as $(newdir) it would run
# in a subshell and the registration would never reach the trap.
newdir() {
    work=$(mktemp -d) || die "mktemp failed"
    dirs+=("$work")
}

done_dir() {
    local d keep=()
    sudo -n rm -rf -- "$1"
    for d in "${dirs[@]}"; do
        [ "$d" = "$1" ] || keep+=("$d")
    done
    dirs=("${keep[@]}")
}

die() { echo "measure: $*" >&2; exit 1; }

# wait_pid_line waits until the burst printed its PID (the file exists as
# soon as the redirect opens it, before the JVM wrote anything).
wait_pid_line() {
    local -i i
    for ((i = 0; i < 1200; i++)); do
        if [[ $(head -1 "$1" 2>/dev/null) =~ ^[0-9]+$ ]]; then
            head -1 "$1"
            return 0
        fi
        sleep 0.1
    done
    die "no PID line in $1"
}

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
    newdir
    java -jar "$jar" syscall-burst --ops "$ops" --threads "$t" --dir "$work" >"$work/out" 2>/dev/null ||
        die "untraced burst failed"
    echo "mode=untraced threads=$t ops=$ops elapsed_ns=$(elapsed_of "$work/out")" >>"$samples"
    done_dir "$work"
}

# traced runs the burst behind its startup gate, traced by its own PID.
# Tracer CPU is utime+stime from /usr/bin/time (the tracer is sudo's
# child, so time wraps the tracer itself).
traced() {
    local t=$1 work pid tpid jpid
    newdir
    IORMESH_WORKLOAD_STARTUP_FILE=$work/gate java -jar "$jar" syscall-burst --ops "$ops" \
        --threads "$t" --dir "$work" >"$work/out" 2>/dev/null &
    jpid=$!
    pids+=("$jpid")
    pid=$(wait_pid_line "$work/out")
    sudo -n /usr/bin/time -f '%U %S' -o "$work/time" "$tracer" run -pid "$pid" -project s3 \
        -out "$work/trace" -duration 10m -ready "$work/ready" 2>"$work/tracer.err" &
    tpid=$!
    pids+=("$tpid")
    wait_file "$work/ready" "$tpid" "$work/tracer.err"
    touch "$work/gate"
    wait "$tpid" || die "tracer failed: $(tail -3 "$work/tracer.err")"
    forget "$tpid"
    wait "$jpid" || die "traced burst failed"
    forget "$jpid"
    traced_line "$t" "$work"
    done_dir "$work"
}

traced_line() {
    local t=$1 work=$2 cpu lost kernel events bytes
    cpu=$(awk '{printf "%d", ($1 + $2) * 1000}' "$work/time")
    # lost counts every record the trace wanted but did not get (kernel
    # ring drops plus undecodable records); kernel is the drop part.
    lost=$(sed -n 's/.*lost records \([0-9][0-9]*\).*/\1/p' "$work/tracer.err")
    kernel=$(sed -n 's/.*(kernel \([0-9][0-9]*\)).*/\1/p' "$work/tracer.err")
    events=$(wc -l <"$work/trace/events.ndjson")
    bytes=$(stat -c %s "$work/trace/events.ndjson")
    echo "mode=traced threads=$t ops=$ops elapsed_ns=$(elapsed_of "$work/out")" \
        "tracer_cpu_ms=$cpu lost=$lost kernel_lost=$kernel events=$events ndjson_bytes=$bytes" >>"$samples"
}

# volume traces one small burst (S3_VOLUME_OPS, default 50000 ops =
# 100k syscalls, below what the ring absorbs) and records its loss and
# the NDJSON's size raw and zstd-compressed — the volume figures and the
# "a short burst loses nothing" observation come from this line.
volume() {
    local vops=${S3_VOLUME_OPS:-50000} work pid tpid jpid f z3 z19
    newdir
    IORMESH_WORKLOAD_STARTUP_FILE=$work/gate java -jar "$jar" syscall-burst --ops "$vops" \
        --threads 1 --dir "$work" >"$work/out" 2>/dev/null &
    jpid=$!
    pids+=("$jpid")
    pid=$(wait_pid_line "$work/out")
    sudo -n "$tracer" run -pid "$pid" -project s3 -out "$work/trace" -duration 10m \
        -ready "$work/ready" 2>"$work/tracer.err" &
    tpid=$!
    pids+=("$tpid")
    wait_file "$work/ready" "$tpid" "$work/tracer.err"
    touch "$work/gate"
    wait "$tpid" || die "volume tracer failed: $(tail -3 "$work/tracer.err")"
    forget "$tpid"
    wait "$jpid" || die "volume burst failed"
    forget "$jpid"
    f=$work/trace/events.ndjson
    z3=$(zstd -q -3 -c "$f" | wc -c)
    z19=$(zstd -q -19 -c "$f" | wc -c)
    echo "mode=volume threads=1 ops=$vops lost=$(sed -n 's/.*lost records \([0-9][0-9]*\).*/\1/p' "$work/tracer.err")" \
        "events=$(wc -l <"$f") ndjson_bytes=$(stat -c %s "$f") zstd3_bytes=$z3 zstd19_bytes=$z19" >>"$samples"
    done_dir "$work"
}

# bystander runs the burst untraced while a tracer is attached to another,
# idle process: the burst's syscalls still hit the tracepoints, which
# filter it out. The difference to untraced is the host-wide tax.
bystander() {
    local t=$1 work idle tpid
    newdir
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
    wait "$idle" 2>/dev/null || true
    forget "$idle"
    wait "$tpid" || die "bystander tracer failed: $(tail -3 "$work/tracer.err")"
    forget "$tpid"
    # events proves the filter: the idle target issues no traced I/O, so
    # only the session/end records appear and the CPU is fixed cost.
    echo "mode=bystander threads=$t ops=$ops elapsed_ns=$(elapsed_of "$work/out")" \
        "tracer_cpu_ms=$(awk '{printf "%d", ($1 + $2) * 1000}' "$work/time")" \
        "events=$(wc -l <"$work/trace/events.ndjson")" >>"$samples"
    done_dir "$work"
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
    volume
}

main

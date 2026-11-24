#!/usr/bin/env bash
# S3 measurement (docs/implementation.md "S3", docs/decisions.md #25):
# target slowdown, bystander cost, tracer CPU, loss and output volume of
# the tracer on the syscall-burst load scenario.
#
# Usage: spikes/s3/measure.sh OUTDIR   (needs sudo -n)
# Env:   S3_OPS (default 200000), S3_THREADS ("1 4 16"), S3_RUNS (3),
#        S3_PAIRS (10), S3_MODES ("traced bystander volume"; "kind"
#        is opt-in), S3_KIND_OPS (5000000),
#        S3_TRACER_CPU (3), S3_LOAD_CPUS ("0-2,4-6"),
#        S3_TRACER_FLAGS (extra `iormesh run` flags, word-split, e.g.
#        "-native-stacks -aggregate=false"; task ua4),
#        S3_AGENT (1: every burst runs under the iormesh agent, and a
#        traced one with -spool, both on a fresh spool dir per run;
#        task ud4), S3_TRACER (the tracer binary, default bin/iormesh: an
#        A/B against another build; task ud4), S3_PERF (1: also count
#        the tracer's instructions from its ready file to its exit with
#        perf stat — a figure host load barely moves, unlike CPU time;
#        task ud4 — and its cycles, user and kernel; task ee4),
#        S3_SEGMENT (1: the traced run writes M4's parquet segments —
#        -trace-dir in its work dir, -disk-limit 0, no -out — and its
#        events are counted from the segments' footers by rowcount.go;
#        task ee4)
#
# The method is load-tolerant (decision #25 as changed 2026-11-19, task
# md4): the host is never quiet, so the gated figures are CPU times, not
# wall clocks, and every process is pinned — the tracer to S3_TRACER_CPU,
# the burst JVM to S3_LOAD_CPUS — so the tracer never competes with the
# load it measures. The defaults fit an 8-CPU host whose CPU k and k+4
# are SMT siblings: the tracer gets core 3, the burst cores 0-2, and
# CPU 7 (core 3's sibling) is left to the rest of the host.
#
# Modes:
#   traced     S3_RUNS x (untraced, traced) per thread count: slowdown,
#              loss, and the tracer's CPU from its ready file to its exit
#              (tracer_run_cpu_ms; load and attach excluded) per event.
#   bystander  S3_PAIRS interleaved pairs per thread count, each an
#              untraced burst then one beside a tracer attached to an
#              idle process (ABAB): the burst's own user+sys CPU time
#              (burst_cpu_ms) and loop wall clock (elapsed_ns).
#   volume     one small traced burst: loss and NDJSON size, raw and zstd.
#   kind       bystander pairs as above, with a getppid loop (getppid.c,
#              S3_KIND_OPS calls per thread) in place of the burst: a
#              syscall of a kind no family traces, which since task 8e4
#              still meets the raw_syscalls dispatchers (task ae4).
#
# Every sample is one line in OUTDIR/samples.txt; summary.txt holds the
# medians (summarize below). Throwaway spike code: the numbers, not the
# script, are the result.
set -euo pipefail

repo=$(cd "$(dirname "$0")/../.." && pwd)
out=${1:?usage: measure.sh OUTDIR}
ops=${S3_OPS:-200000}
threads=${S3_THREADS:-1 4 16}
runs=${S3_RUNS:-3}
pairs=${S3_PAIRS:-10}
modes=${S3_MODES:-traced bystander volume}
kind_ops=${S3_KIND_OPS:-5000000}
tracer_cpu=${S3_TRACER_CPU:-3}
load_cpus=${S3_LOAD_CPUS:-0-2,4-6}
# shellcheck disable=SC2206 # word splitting is the point: one flag per word
tracer_flags=(${S3_TRACER_FLAGS:-})
jar=$repo/bin/workload.jar
agent_jar=$repo/bin/iormesh-agent.jar
agent=${S3_AGENT:-0}
tracer=${S3_TRACER:-$repo/bin/iormesh}
perf=${S3_PERF:-0}
segment=${S3_SEGMENT:-0}
clk_tck=$(getconf CLK_TCK)
mkdir -p "$out"
samples=$out/samples.txt
# pids and dirs hold only what is still live: forget() drops a pid once
# it was waited for, so the trap never signals a recycled PID.
pids=()
dirs=()

# cleanup sends TERM, not INT: background children of a non-interactive
# shell start with SIGINT ignored. For a tracer, pids holds both the sudo
# job and the tracer's own PID: sudo need not relay a signal sent from
# its own process group, and GNU time does not forward TERM, so only a
# TERM to the tracer itself is sure to stop it (and detach its probes).
# A tracer still loading has no PID in pids yet (tracer_start learns it
# at ready), so cleanup also stops any tracer whose -ready file lies in a
# live work dir, and waits for it before removing the dir — otherwise it
# would recreate the dir as root when it gets ready.
cleanup() {
    local p d
    for p in "${pids[@]}"; do
        sudo -n kill -TERM "$p" 2>/dev/null || true
    done
    for d in "${dirs[@]}"; do
        _stop_tracers_in "$d"
        sudo -n rm -rf -- "$d"
    done
}
trap cleanup EXIT

# _stop_tracers_in TERMs the tracers whose -ready file is in dir and
# waits up to 10 s for them to exit. The -ready path is unique per run,
# so no other user's iormesh is ever matched.
_stop_tracers_in() {
    local re="^$tracer run .*-ready $1/ready"
    local -i i
    sudo -n pkill -TERM -f -- "$re" 2>/dev/null || return 0
    for ((i = 0; i < 100; i++)); do
        pgrep -f -- "$re" >/dev/null || return 0
        sleep 0.1
    done
}

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

# wait_line waits until file's first line matches regex and prints it.
# The burst's stdout exists as soon as the redirect opens it, before the
# JVM wrote anything, so existence alone says nothing.
wait_line() {
    local file=$1 re=$2 line
    local -i i
    for ((i = 0; i < 1200; i++)); do
        line=$(grep -m1 -E "$re" "$file" 2>/dev/null || true)
        if [ -n "$line" ]; then
            echo "$line"
            return 0
        fi
        sleep 0.05
    done
    die "no line matching '$re' in $file"
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

# cpu_ms prints a live process's user+sys CPU time in ms, summed over
# all its threads (fields 14 and 15 of /proc/PID/stat, in clock ticks;
# the comm field holds no spaces for java or iormesh). The file is
# world-readable, so this works on the root-owned tracer too.
cpu_ms() {
    awk -v hz="$clk_tck" '{printf "%d", ($14 + $15) * 1000 / hz}' "/proc/$1/stat"
}

# elapsed_of prints elapsed_ns from a syscall-burst stdout line.
elapsed_of() {
    sed -n 's/^syscall-burst .* elapsed_ns=\([0-9][0-9]*\)$/\1/p' <<<"$1"
}

loadavg() { cut -d' ' -f1 /proc/loadavg; }

# burst_start starts the burst JVM pinned to the load CPUs, held at its
# startup gate, and sets the globals jpid (the background job) and bpid
# (the PID the burst printed, the one a tracer follows). The hold file
# keeps the JVM alive after its loops, so burst_finish can read its CPU
# time from /proc before it exits.
#
# With S3_AGENT=1 the JVM loads the agent with a ring in $dir/spool,
# which the tracer must own (root) and which must exist before the JVM
# starts: the agent maps its ring in premain. A fresh dir per run keeps
# the joiner from opening the rings of earlier runs' JVMs.
burst_start() {
    local t=$1 dir=$2 jto=${JAVA_TOOL_OPTIONS:-}
    if [ "$agent" = 1 ]; then
        sudo -n mkdir -m 1733 "$dir/spool"
        jto="-javaagent:$agent_jar=spool=$dir/spool"
    fi
    JAVA_TOOL_OPTIONS=$jto \
        IORMESH_WORKLOAD_STARTUP_FILE=$dir/gate IORMESH_WORKLOAD_HOLD_FILE=$dir/hold \
        taskset -c "$load_cpus" java -jar "$jar" syscall-burst --ops "$ops" \
        --threads "$t" --dir "$dir" >"$dir/out" 2>/dev/null &
    jpid=$!
    pids+=("$jpid")
    bpid=$(wait_line "$dir/out" '^[0-9]+$')
}

# burst_finish opens the gate and waits for the loops; it sets burst_cpu
# (the JVM's user+sys ms from the gate to the result line: file opens,
# loops and their JIT, not JVM startup) and burst_elapsed (the loops'
# wall ns, as the burst reports it).
burst_finish() {
    local dir=$1 cpu0 line
    cpu0=$(cpu_ms "$bpid")
    touch "$dir/gate"
    line=$(wait_line "$dir/out" '^syscall-burst ')
    burst_cpu=$(($(cpu_ms "$bpid") - cpu0))
    burst_elapsed=$(elapsed_of "$line")
    touch "$dir/hold"
    wait "$jpid" || die "burst failed"
    forget "$jpid"
}

# tracer_start attaches a pinned tracer to pid and waits until it is
# ready. It sets tpid (the sudo job), ipid (the tracer's own PID, which
# cleanup signals) and ready_cpu (the tracer's CPU ms at ready: object
# load and attach, the fixed cost that tracer_run_cpu_ms leaves out).
# The tracer is time's child; its own PID is found by its command line,
# which starts with the binary's path (time's does not).
tracer_start() {
    local pid=$1 dir=$2 spool=() sink=()
    sink=(-out "$dir/trace")
    [ "$agent" = 1 ] && [ -d "$dir/spool" ] && spool=(-spool "$dir/spool")
    # The disk watchdog is off (-disk-limit 0): it gates the partition's
    # used percent, and a developer's root may be fuller than its 90 %
    # default; the free-bytes floor stays.
    [ "$segment" = 1 ] && sink=(-trace-dir "$dir/td" -disk-limit 0)
    sudo -n taskset -c "$tracer_cpu" /usr/bin/time -f '%U %S' -o "$dir/time" \
        "$tracer" run -pid "$pid" -project s3 "${sink[@]}" -duration 10m \
        -ready "$dir/ready" "${tracer_flags[@]}" "${spool[@]}" 2>"$dir/tracer.err" &
    tpid=$!
    pids+=("$tpid")
    wait_file "$dir/ready" "$tpid" "$dir/tracer.err"
    ipid=$(pgrep -f -- "^$tracer run -pid $pid .*-ready $dir/ready") ||
        die "tracer process not found"
    pids+=("$ipid")
    ready_cpu=$(cpu_ms "$ipid")
    if [ "$perf" = 1 ]; then
        # perf follows the tracer from here and exits with it.
        sudo -n perf stat -x, -e instructions:u,instructions:k,cycles:u,cycles:k -p "$ipid" \
            -o "$dir/perf" 2>/dev/null &
    fi
}

# tracer_wait waits for the tracer to exit (it does once its target
# exited) and sets tracer_total (its whole user+sys ms, from time).
tracer_wait() {
    local dir=$1
    wait "$tpid" || die "tracer failed: $(tail -3 "$dir/tracer.err")"
    forget "$tpid"
    # The tracer exited before its sudo and time parents did.
    forget "$ipid"
    tracer_total=$(awk '{printf "%d", ($1 + $2) * 1000}' "$dir/time")
    insn_u=0 insn_k=0 cyc_u=0 cyc_k=0
    if [ "$perf" = 1 ]; then
        local -i i
        for ((i = 0; i < 100; i++)); do
            grep -q 'cycles:k' "$dir/perf" 2>/dev/null && break
            sleep 0.1
        done
        insn_u=$(awk -F, '/instructions:u/{print $1}' "$dir/perf" 2>/dev/null)
        insn_k=$(awk -F, '/instructions:k/{print $1}' "$dir/perf" 2>/dev/null)
        cyc_u=$(awk -F, '/cycles:u/{print $1}' "$dir/perf" 2>/dev/null)
        cyc_k=$(awk -F, '/cycles:k/{print $1}' "$dir/perf" 2>/dev/null)
    fi
}

untraced() {
    local t=$1 tag=$2 work
    newdir
    burst_start "$t" "$work"
    burst_finish "$work"
    echo "mode=untraced threads=$t ops=$ops $tag elapsed_ns=$burst_elapsed" \
        "burst_cpu_ms=$burst_cpu load=$(loadavg)" >>"$samples"
    done_dir "$work"
}

# traced runs the burst traced by its own PID. tracer_run_cpu_ms is the
# tracer's CPU from its ready file to its exit — the per-event figure's
# numerator (wc4's method); tracer_cpu_ms is the whole run, for
# comparison with older samples.
traced() {
    local t=$1 tag=$2 work
    newdir
    burst_start "$t" "$work"
    tracer_start "$bpid" "$work"
    burst_finish "$work"
    tracer_wait "$work"
    traced_line "$t" "$tag" "$work"
    done_dir "$work"
}

traced_line() {
    local t=$1 tag=$2 work=$3 lost kernel skipped attributed events bytes
    # lost counts every record the trace wanted but did not get (kernel
    # ring drops plus undecodable records); kernel is the drop part.
    # skipped is the program runs the kernel skipped host-wide: possible loss,
    # not in lost (docs/output-contract.md, task vd4).
    lost=$(sed -n 's/.*lost records \([0-9][0-9]*\).*/\1/p' "$work/tracer.err")
    kernel=$(sed -n 's/.*(kernel \([0-9][0-9]*\)).*/\1/p' "$work/tracer.err")
    skipped=$(sed -n 's/.*skipped runs \([0-9][0-9]*\).*/\1/p' "$work/tracer.err")
    # attributed is the Java join's count (-spool runs; 0 otherwise). A
    # stale iormesh-agent.jar writes a ring version the tracer skips, and
    # the run then measures no join at all: rebuild it (mage agentBuild).
    attributed=$(sed -n 's/.*, attributed \([0-9][0-9]*\).*/\1/p' "$work/tracer.err")
    if [ "$agent" = 1 ] && [ "${attributed:-0}" = 0 ]; then
        echo "measure: agent on, but no event attributed: stale agent jar?" >&2
    fi
    if [ "$segment" = 1 ]; then
        # bytes is then the published segments' size (zstd pages): not
        # comparable with ndjson_bytes of an -out run.
        events=$("$rowcount" "$work/td/s3")
        bytes=$(cat "$work"/td/s3/ready/*.parquet | wc -c)
    else
        events=$(wc -l <"$work/trace/events.ndjson")
        bytes=$(stat -c %s "$work/trace/events.ndjson")
    fi
    echo "mode=traced threads=$t ops=$ops $tag elapsed_ns=$burst_elapsed" \
        "burst_cpu_ms=$burst_cpu tracer_cpu_ms=$tracer_total" \
        "tracer_run_cpu_ms=$((tracer_total - ready_cpu)) lost=$lost" \
        "kernel_lost=$kernel skipped_runs=$skipped events=$events ndjson_bytes=$bytes" \
        "attributed=${attributed:-0} insn_u=${insn_u:-0} insn_k=${insn_k:-0}" \
        "cyc_u=${cyc_u:-0} cyc_k=${cyc_k:-0}" \
        "load=$(loadavg)" >>"$samples"
}

# bystander runs the burst untraced while a tracer is attached to another,
# idle process: the burst's syscalls still hit the tracepoints, which
# filter it out. The difference to untraced is the host-wide tax.
bystander() {
    local t=$1 tag=$2 work idle
    newdir
    sleep 600 &
    idle=$!
    pids+=("$idle")
    tracer_start "$idle" "$work"
    burst_start "$t" "$work"
    burst_finish "$work"
    kill "$idle"
    wait "$idle" 2>/dev/null || true
    forget "$idle"
    tracer_wait "$work"
    # events proves the filter: the idle target issues no traced I/O, so
    # only a handful of session/control records appear (2-5 seen).
    echo "mode=bystander threads=$t ops=$ops $tag elapsed_ns=$burst_elapsed" \
        "burst_cpu_ms=$burst_cpu tracer_cpu_ms=$tracer_total" \
        "events=$(wc -l <"$work/trace/events.ndjson") load=$(loadavg)" >>"$samples"
    done_dir "$work"
}

# volume traces one small burst (S3_VOLUME_OPS, default 50000 ops =
# 100k syscalls, below what the ring absorbs) and records its loss and
# the NDJSON's size raw and zstd-compressed — the volume figures and the
# "a short burst loses nothing" observation come from this line. ops is
# shadowed, not reassigned: the burst helpers read it dynamically.
volume() {
    local ops=${S3_VOLUME_OPS:-50000} work f z3 z19
    newdir
    burst_start 1 "$work"
    tracer_start "$bpid" "$work"
    burst_finish "$work"
    tracer_wait "$work"
    f=$work/trace/events.ndjson
    z3=$(zstd -q -3 -c "$f" | wc -c)
    z19=$(zstd -q -19 -c "$f" | wc -c)
    echo "mode=volume threads=1 ops=$ops" \
        "lost=$(sed -n 's/.*lost records \([0-9][0-9]*\).*/\1/p' "$work/tracer.err")" \
        "skipped_runs=$(sed -n 's/.*skipped runs \([0-9][0-9]*\).*/\1/p' "$work/tracer.err")" \
        "events=$(wc -l <"$f") ndjson_bytes=$(stat -c %s "$f")" \
        "zstd3_bytes=$z3 zstd19_bytes=$z19" >>"$samples"
    done_dir "$work"
}

# kind_loop runs the getppid loop pinned to the load CPUs and sets
# burst_elapsed and burst_cpu from its result line (the loop reports its
# own CPU time: it starts in a few hundred microseconds, so no gate).
kind_loop() {
    local t=$1 line
    line=$(taskset -c "$load_cpus" "$kind_bin" "$t" "$kind_ops") ||
        die "getppid loop failed"
    burst_elapsed=$(sed -n 's/.* elapsed_ns=\([0-9]*\) .*/\1/p' <<<"$line")
    burst_cpu=$(sed -n 's/.* cpu_ms=\([0-9]*\)$/\1/p' <<<"$line")
}

# kind_pair takes one ABAB pair of the getppid loop: untraced, then beside
# a tracer attached to an idle process, as bystander does for the burst.
kind_pair() {
    local t=$1 tag=$2 work idle
    kind_loop "$t"
    echo "mode=kuntraced threads=$t ops=$kind_ops $tag" \
        "elapsed_ns=$burst_elapsed burst_cpu_ms=$burst_cpu load=$(loadavg)" >>"$samples"
    newdir
    sleep 600 &
    idle=$!
    pids+=("$idle")
    tracer_start "$idle" "$work"
    kind_loop "$t"
    kill "$idle"
    wait "$idle" 2>/dev/null || true
    forget "$idle"
    tracer_wait "$work"
    echo "mode=kbystander threads=$t ops=$kind_ops $tag" \
        "elapsed_ns=$burst_elapsed burst_cpu_ms=$burst_cpu" \
        "tracer_cpu_ms=$tracer_total load=$(loadavg)" >>"$samples"
    done_dir "$work"
}

# run_kind builds the getppid loop into a work dir (gone with the run) and
# takes S3_PAIRS pairs of it per thread count.
run_kind() {
    local -i p
    local t
    newdir
    kind_bin=$work/getppid
    cc -O2 -pthread -o "$kind_bin" "$repo/spikes/s3/getppid.c" ||
        die "cannot build getppid.c"
    for t in $threads; do
        for ((p = 1; p <= pairs; p++)); do
            kind_pair "$t" "pair=$p"
        done
    done
}

# summarize writes the medians decision #25 is judged by: per thread
# count, the tracer's ns per emitted event (ready to exit), the traced
# slowdown, and the bystander ratios — each pair's bystander over its
# untraced burst, on CPU time (gated) and on wall clock (recorded) —
# with the pairs' load-average range (recorded, not gated). The kind
# mode's getppid pairs get a line of their own, kind_cpu and kind_wall.
summarize() {
    gawk '
    function median(a, n,   s) {
        if (n == 0) return "-"
        asort(a, s, "@val_num_asc")
        return n % 2 ? s[(n + 1) / 2] : (s[n / 2] + s[n / 2 + 1]) / 2
    }
    /^mode=/ {
        delete f
        for (i = 1; i <= NF; i++) { split($i, kv, "="); f[kv[1]] = kv[2] }
        t = f["threads"]; k = t SUBSEP "r" f["run"] SUBSEP "p" f["pair"]
        if (f["mode"] == "traced" && f["events"] > 0) {
            ns[t][++nn[t]] = f["tracer_run_cpu_ms"] * 1e6 / f["events"]
            tr[k] = f["elapsed_ns"]
            if (f["insn_u"] > 0) is[t][++ni[t]] = (f["insn_u"] + f["insn_k"]) / f["events"]
            if (f["cyc_u"] > 0) cy[t][++nc[t]] = (f["cyc_u"] + f["cyc_k"]) / f["events"]
        }
        if (f["mode"] == "untraced") { ue[k] = f["elapsed_ns"]; uc[k] = f["burst_cpu_ms"] }
        if (f["mode"] == "bystander") {
            bc[t][++nb[t]] = f["burst_cpu_ms"] / uc[k]
            be[t][nb[t]] = f["elapsed_ns"] / ue[k]
            if (!(t in lo) || f["load"] < lo[t]) lo[t] = f["load"]
            if (f["load"] > hi[t]) hi[t] = f["load"]
        }
        if (f["mode"] == "traced") sl[t][++ns2[t]] = f["elapsed_ns"] / ue[k]
        if (f["mode"] == "kuntraced") { kue[k] = f["elapsed_ns"]; kuc[k] = f["burst_cpu_ms"] }
        if (f["mode"] == "kbystander") {
            kc[t][++nk[t]] = f["burst_cpu_ms"] / kuc[k]
            ke[t][nk[t]] = f["elapsed_ns"] / kue[k]
        }
        if (f["mode"] !~ /^k/) th[t] = 1
        else kt[t] = 1
    }
    END {
        for (t in th) {
            printf "threads=%s tracer_ns_per_event=%s (n=%d) slowdown=%s", \
                t, median(ns[t], nn[t]), nn[t], median(sl[t], ns2[t])
            if (ni[t] > 0) printf " tracer_insn_per_event=%s", median(is[t], ni[t])
            if (nc[t] > 0) printf " tracer_cycles_per_event=%s", median(cy[t], nc[t])
            printf " bystander_cpu=%s bystander_wall=%s (pairs=%d load=%s-%s)\n", \
                median(bc[t], nb[t]), median(be[t], nb[t]), nb[t], lo[t], hi[t]
        }
        for (t in kt)
            printf "kind=getppid threads=%s kind_cpu=%s kind_wall=%s (pairs=%d)\n", \
                t, median(kc[t], nk[t]), median(ke[t], nk[t]), nk[t]
    }' "$samples" >"$out/summary.txt"
}

# header marks the commit "+dirty" when measure.sh itself differs from
# it, so a sample file never claims a script version it was not taken by.
header() {
    local rev
    rev=$(git -C "$repo" rev-parse --short HEAD)
    git -C "$repo" diff --quiet HEAD -- spikes/s3/measure.sh || rev+=+dirty
    {
        echo "# S3 samples $(date -u +%Y-%m-%dT%H:%M:%SZ) commit $rev"
        echo "# kernel $(uname -r); cpu $(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2- | xargs) x$(nproc)"
        echo "# java $(java -version 2>&1 | head -1); workdir fs $(df -T "${TMPDIR:-/tmp}" | awk 'NR==2{print $2}')"
        echo "# ops=$ops threads=$threads runs=$runs pairs=$pairs modes=$modes" \
            "kind_ops=$kind_ops tracer_cpu=$tracer_cpu load_cpus=$load_cpus" \
            "tracer_flags=${tracer_flags[*]} agent=$agent perf=$perf segment=$segment" \
            "tracer=$tracer"
    } >>"$samples"
}

# run_traced takes S3_RUNS rounds of an untraced and a traced burst per
# thread count; run=R pairs them for the slowdown ratio.
run_traced() {
    local -i r
    local t
    for ((r = 1; r <= runs; r++)); do
        for t in $threads; do
            untraced "$t" "run=$r"
            traced "$t" "run=$r"
        done
    done
}

# run_bystander takes S3_PAIRS interleaved pairs per thread count, the
# untraced burst first in each (ABAB), so slow drift in the host's load
# lands on both halves of a pair instead of on one mode.
run_bystander() {
    local -i p
    local t
    for t in $threads; do
        for ((p = 1; p <= pairs; p++)); do
            untraced "$t" "pair=$p"
            bystander "$t" "pair=$p"
        done
    done
}

main() {
    [ -x "$tracer" ] && [ -f "$jar" ] || die "run 'mage build workloadBuild' first"
    if [ "$segment" = 1 ]; then
        # Built once per measurement into a dir of its own (gone with it).
        newdir
        rowcount=$work/rowcount
        (cd "$repo" && go build -o "$rowcount" spikes/s3/rowcount.go) ||
            die "cannot build rowcount.go"
    fi
    header
    local m
    for m in $modes; do
        case $m in
        traced) run_traced ;;
        bystander) run_bystander ;;
        volume) volume ;;
        kind) run_kind ;;
        *) die "unknown mode $m" ;;
        esac
    done
    summarize
}

main

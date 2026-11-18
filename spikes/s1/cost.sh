#!/usr/bin/env bash
# Throwaway (S1, task xa4): cost per I/O op of the S1 join on
# syscall-burst (two hooked syscalls per op: FileOutputStream.write(int),
# FileInputStream.read()). Runs each mode REPS times, interleaved, and
# prints one line per run: mode, 1-min loadavg, wall ns per op
# (elapsed_ns / ops, the loops only), the JVM's total CPU in ms
# (user + sys, startup included), and the uprobe/syscall counts.
# Wall time swings with host load; costper.py turns the CPU column of
# two runs with different --ops into CPU ns per op, which cancels the
# startup and is far less sensitive to other tenants.
#
# Modes: untraced | sys (syscall tracepoint only) | uprobe (sys + hook
# marks) | noop (agent, empty hooks) | nostack (agent ring, no stack
# walk) | ring (agent ring + stack walk) | file (agent append spool) |
# all (agent ring + uprobe marks).
#
# Usage: [HOOKS=file] cost.sh <JAVA_HOME> <agent dir> <ops> <threads> <reps> <outdir> [modes...]
# HOOKS defaults to hooks-burst.txt (the natives the burst hits).
set -euo pipefail

readonly jh="${1:?usage: cost.sh JAVA_HOME AGENTDIR OPS THREADS REPS OUTDIR [MODES]}"
readonly ad="${2:?agent dir}"
readonly ops="${3:?ops}"
readonly threads="${4:?threads}"
readonly reps="${5:?reps}"
readonly out="${6:?outdir}"
shift 6
modes=("$@")
((${#modes[@]})) || modes=(untraced sys uprobe noop nostack ring file all)
here="$(cd "$(dirname "$0")" && pwd)"
readonly here
readonly jar="$here/../../workload.jar"
readonly hooks="${HOOKS:-$here/hooks-burst.txt}"

mkdir -p "$out"
bt_pid=""
jvm_pid=""

# probe_list <uprobe|uretprobe>: the hook set as bpftrace attach points.
probe_list() {
  grep -v -e '^#' -e '^$' "$hooks" \
    | awk -v kind="$1" -v lib="$jh/lib" '{ print kind ":" lib "/" $1 ":" $2 }' \
    | paste -sd, -
}

# render <marks yes|no> <file>: cost.bt with or without the uprobes.
render() {
  python3 - "$here/cost.bt.in" "$1" "$(probe_list uprobe)" "$(probe_list uretprobe)" \
    > "$2" <<'PY'
import re, sys
src, marks, entry, ret = sys.argv[1:]
s = open(src).read()
if marks == "no":
    # Drop both uprobe blocks: placeholder line through its closing brace.
    s = re.sub(r"@(ENTRY|RETURN)@\n.*?\n}\n", "", s, flags=re.S)
print(s.replace("@ENTRY@", entry).replace("@RETURN@", ret), end="")
PY
}
render no "$out/sys.bt"
render yes "$out/uprobe.bt"

# cleanup: EXIT trap — no gated JVM or root bpftrace may outlive a run.
cleanup() {
  [[ -n "$jvm_pid" ]] && kill "$jvm_pid" 2>/dev/null || true
  if [[ -n "$bt_pid" ]]; then
    sudo -n pkill -INT -f "bpftrace -B line $out/" || true
    sleep 5
    sudo -n pkill -KILL -f "bpftrace -B line $out/" || true
  fi
  wait || true
}
trap cleanup EXIT

# java_opts <mode>: the agent flag for the agent modes, else nothing.
java_opts() {
  local a="$ad/agent.jar=$ad/bridge.jar,$out/run"
  case "$1" in
    noop) echo "-javaagent:$a,noop" ;;
    nostack) echo "-javaagent:$a,ring,nostack" ;;
    ring | all) echo "-javaagent:$a,ring" ;;
    file) echo "-javaagent:$a,file" ;;
  esac
}

# bt_script <mode>: the bpftrace script a mode runs under, or nothing.
bt_script() {
  case "$1" in
    sys) echo "$out/sys.bt" ;;
    uprobe | all) echo "$out/uprobe.bt" ;;
  esac
}

# run_one <mode>: one gated burst; prints "<mode> <load1> <ns/op>".
run_one() {
  local mode="$1" bt line
  rm -f "$out/gate" "$out/run.stdout"
  # shellcheck disable=SC2046  # java_opts is a word list on purpose
  IORMESH_WORKLOAD_STARTUP_FILE="$out/gate" /usr/bin/time -f "%U %S" -o "$out/time" \
    "$jh/bin/java" $(java_opts "$mode") \
    -jar "$jar" syscall-burst --ops "$ops" --threads "$threads" --dir "$out" \
    > "$out/run.stdout" 2> "$out/run.stderr" &
  jvm_pid="$!"
  for _ in $(seq 200); do [[ -s "$out/run.stdout" ]] && break; sleep 0.05; done
  bt="$(bt_script "$mode")"
  if [[ -n "$bt" ]]; then
    # Remove the last run's log first: the background redirect truncates
    # it only when the subshell gets there, and a stale READY would race.
    rm -f "$out/bt.log"
    # shellcheck disable=SC2024  # log stays owned by the invoking user
    # -B line: READY must reach the file now, not at exit.
    sudo -n bpftrace -B line "$bt" "$(head -1 "$out/run.stdout")" > "$out/bt.log" 2>&1 &
    bt_pid="$!"
    for _ in $(seq 600); do grep -qs READY "$out/bt.log" && break; sleep 0.1; done
    if ! grep -q READY "$out/bt.log"; then
      echo "bpftrace failed for $mode (see $out/bt.log)" >&2
      return 1
    fi
  fi
  touch "$out/gate"
  wait "$jvm_pid"
  jvm_pid=""
  if [[ -n "$bt" ]]; then
    sudo -n pkill -INT -f "bpftrace -B line $bt" || true
    wait "$bt_pid" || true
    bt_pid=""
    grep '^@sys' "$out/bt.log" | paste -sd' ' - > "$out/bt.counts" || true
  fi
  line="$(grep elapsed_ns "$out/run.stdout")"
  echo "$mode $(cut -d' ' -f1 /proc/loadavg) $((${line##*elapsed_ns=} / ops))" \
    "$(awk '{ printf "%d", ($1 + $2) * 1000 }' "$out/time")" \
    "$( [[ -n "$bt" ]] && cat "$out/bt.counts")"
}

for ((r = 0; r < reps; r++)); do
  for m in "${modes[@]}"; do
    run_one "$m"
  done
done
